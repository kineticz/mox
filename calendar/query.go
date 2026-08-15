package calendar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/store"
)

type Instance struct {
	Event     store.CalendarEvent
	Start     icalendar.DateTime
	End       icalendar.DateTime
	Exception bool
}

func query(ctx context.Context, acc *store.Account, from, to time.Time) (instances []Instance, err error) {
	err = acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		cal, err := acc.DefaultCalendar(tx)
		if err != nil {
			return err
		}

		recurring, err := bstore.QueryTx[store.CalendarEvent](tx).
			FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
			FilterEqual("RecurrenceID", "").
			FilterNotEqual("RRULE", "").
			List()
		if err != nil {
			return err
		}
		rdateOnly, err := bstore.QueryTx[store.CalendarEvent](tx).
			FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
			FilterEqual("RecurrenceID", "").
			FilterEqual("RRULE", "").
			FilterFn(func(ev store.CalendarEvent) bool { return len(ev.RDATE) > 0 }).
			List()
		if err != nil {
			return err
		}
		nonRecurring, err := bstore.QueryTx[store.CalendarEvent](tx).
			FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
			FilterEqual("RecurrenceID", "").
			FilterEqual("RRULE", "").
			FilterLess("StartUTC", to).
			FilterGreater("EndUTC", from.AddDate(0, 0, -1)).
			List()
		if err != nil {
			return err
		}
		for _, ev := range nonRecurring {
			if len(ev.RDATE) > 0 {
				continue
			}
			inst := eventInstance(ev, false)
			if overlaps(inst.Start.UTC, inst.End.UTC, inst.Start.DateOnly, from, to) {
				instances = append(instances, inst)
			}
		}
		var generated []Instance
		recurringUIDs := map[string]string{}
		for _, ev := range append(append([]store.CalendarEvent{}, recurring...), rdateOnly...) {
			recurringUIDs[ev.UID] = ev.TZID
			expanded, err := expandMaster(ev, from, to)
			if err != nil {
				return err
			}
			generated = append(generated, expanded...)
		}

		exceptions := map[string][]icalendar.DateTime{}
		if len(generated) > 0 {
			uids := make([]any, 0, len(recurringUIDs))
			for uid := range recurringUIDs {
				uids = append(uids, uid)
			}
			matchingExceptions, err := bstore.QueryTx[store.CalendarEvent](tx).
				FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
				FilterEqual("UID", uids...).
				FilterNotEqual("RecurrenceID", "").
				List()
			if err != nil {
				return err
			}
			for _, ev := range matchingExceptions {
				rid, err := parseRecurrenceID(ev.RecurrenceID, recurringUIDs[ev.UID])
				if err != nil {
					continue
				}
				exceptions[ev.UID] = append(exceptions[ev.UID], rid)
			}
		}
		for _, instance := range generated {
			if !exceptionReplaces(exceptions[instance.Event.UID], instance.Start) {
				instances = append(instances, instance)
			}
		}

		overlappingExceptions, err := bstore.QueryTx[store.CalendarEvent](tx).
			FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
			FilterNotEqual("RecurrenceID", "").
			FilterLess("StartUTC", to).
			FilterGreater("EndUTC", from.AddDate(0, 0, -1)).
			List()
		if err != nil {
			return err
		}
		for _, ev := range overlappingExceptions {
			inst := eventInstance(ev, true)
			if overlaps(inst.Start.UTC, inst.End.UTC, inst.Start.DateOnly, from, to) {
				instances = append(instances, inst)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(instances, func(i, j int) bool {
		if !instances[i].Start.UTC.Equal(instances[j].Start.UTC) {
			return instances[i].Start.UTC.Before(instances[j].Start.UTC)
		}
		return instances[i].Event.UID < instances[j].Event.UID
	})
	return instances, nil
}

func eventInstance(ev store.CalendarEvent, exception bool) Instance {
	return Instance{
		Event: ev,
		Start: icalendar.DateTime{
			UTC:      ev.StartUTC,
			TZID:     ev.TZID,
			DateOnly: ev.DateOnly,
			Floating: ev.Floating,
		},
		End: icalendar.DateTime{
			UTC:      ev.EndUTC,
			TZID:     ev.TZID,
			DateOnly: ev.DateOnly,
			Floating: ev.Floating,
		},
		Exception: exception,
	}
}

func expandMaster(ev store.CalendarEvent, from, to time.Time) ([]Instance, error) {
	var recurrence icalendar.Recurrence
	if ev.RRULE != "" {
		var err error
		recurrence, err = icalendar.ParseRRULE(ev.RRULE)
		if err != nil {
			return nil, err
		}
	}
	rdates, err := parseDateValues("RDATE", ev.RDATE)
	if err != nil {
		return nil, err
	}
	exdates, err := parseDateValues("EXDATE", ev.EXDATE)
	if err != nil {
		return nil, err
	}
	start := icalendar.DateTime{
		UTC:      ev.StartUTC,
		TZID:     ev.TZID,
		DateOnly: ev.DateOnly,
		Floating: ev.Floating,
	}
	starts, err := icalendar.Expand(start, recurrence, rdates, exdates, expandFrom(from, ev), to, Limits().MaxRecurrenceInstances)
	if errors.Is(err, icalendar.ErrExpansionLimit) {
		return nil, fmt.Errorf("%w: %v", ErrLimit, err)
	}
	if err != nil {
		return nil, err
	}
	duration := ev.EndUTC.Sub(ev.StartUTC)
	var generated []Instance
	for _, instanceStart := range starts {
		instanceEnd := instanceStart
		instanceEnd.UTC = instanceStart.UTC.Add(duration)
		if overlaps(instanceStart.UTC, instanceEnd.UTC, instanceStart.DateOnly, from, to) {
			generated = append(generated, Instance{Event: ev, Start: instanceStart, End: instanceEnd})
		}
	}
	return generated, nil
}

func expandFrom(from time.Time, ev store.CalendarEvent) time.Time {
	if ev.DateOnly && !ev.EndUTC.After(ev.StartUTC) {
		return from.AddDate(0, 0, -1)
	}
	return from.Add(-ev.EndUTC.Sub(ev.StartUTC))
}

func parseRecurrenceID(value, tzid string) (icalendar.DateTime, error) {
	p := icalendar.Property{Name: "RECURRENCE-ID", Value: value}
	if len(value) == len("20060102") {
		p.Params = []icalendar.Param{{Name: "VALUE", Values: []string{"DATE"}}}
	} else if !strings.HasSuffix(strings.ToUpper(value), "Z") && tzid != "" {
		p.Params = []icalendar.Param{{Name: "TZID", Values: []string{tzid}}}
	}
	return icalendar.ParseDateTime(p)
}

func exceptionReplaces(rids []icalendar.DateTime, instance icalendar.DateTime) bool {
	for _, rid := range rids {
		if sameRecurrence(instance, rid) {
			return true
		}
	}
	return false
}

func sameRecurrence(instance, rid icalendar.DateTime) bool {
	if instance.DateOnly || rid.DateOnly {
		return instance.UTC.UTC().Format("20060102") == rid.UTC.UTC().Format("20060102")
	}
	return instance.UTC.Equal(rid.UTC)
}

func occupiedEnd(start, end time.Time, dateOnly bool) time.Time {
	if dateOnly && !end.After(start) {
		return start.AddDate(0, 0, 1)
	}
	return end
}

func overlaps(start, end time.Time, dateOnly bool, from, to time.Time) bool {
	return start.Before(to) && occupiedEnd(start, end, dateOnly).After(from)
}
