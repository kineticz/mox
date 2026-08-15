package calendar

import (
	"context"
	"sort"
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
		nonRecurring, err := bstore.QueryTx[store.CalendarEvent](tx).
			FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
			FilterEqual("RecurrenceID", "").
			FilterEqual("RRULE", "").
			FilterLess("StartUTC", to).
			FilterGreater("EndUTC", from).
			List()
		if err != nil {
			return err
		}
		for _, ev := range nonRecurring {
			instances = append(instances, eventInstance(ev, false))
		}
		var generated []Instance
		recurrenceIDs := map[string]bool{}
		recurringUIDs := map[string]bool{}
		for _, ev := range recurring {
			recurringUIDs[ev.UID] = true
			recurrence, err := icalendar.ParseRRULE(ev.RRULE)
			if err != nil {
				return err
			}
			rdates, err := parseDateValues("RDATE", ev.RDATE)
			if err != nil {
				return err
			}
			exdates, err := parseDateValues("EXDATE", ev.EXDATE)
			if err != nil {
				return err
			}
			start := icalendar.DateTime{
				UTC:      ev.StartUTC,
				TZID:     ev.TZID,
				DateOnly: ev.DateOnly,
				Floating: ev.Floating,
			}
			duration := ev.EndUTC.Sub(ev.StartUTC)
			starts, err := icalendar.Expand(start, recurrence, rdates, exdates, from.Add(-duration), to, Limits().MaxRecurrenceInstances)
			if err != nil {
				return err
			}
			for _, instanceStart := range starts {
				instanceEnd := instanceStart
				instanceEnd.UTC = instanceStart.UTC.Add(duration)
				if overlaps(instanceStart.UTC, instanceEnd.UTC, from, to) {
					generated = append(generated, Instance{Event: ev, Start: instanceStart, End: instanceEnd})
					recurrenceIDs[instanceStart.Property("RECURRENCE-ID").Value] = true
				}
			}
		}

		exceptions := map[string]map[string]bool{}
		if len(recurrenceIDs) > 0 {
			uids := make([]any, 0, len(recurringUIDs))
			for uid := range recurringUIDs {
				uids = append(uids, uid)
			}
			rids := make([]any, 0, len(recurrenceIDs))
			for rid := range recurrenceIDs {
				rids = append(rids, rid)
			}
			matchingExceptions, err := bstore.QueryTx[store.CalendarEvent](tx).
				FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
				FilterEqual("UID", uids...).
				FilterEqual("RecurrenceID", rids...).
				List()
			if err != nil {
				return err
			}
			for _, ev := range matchingExceptions {
				if exceptions[ev.UID] == nil {
					exceptions[ev.UID] = map[string]bool{}
				}
				exceptions[ev.UID][ev.RecurrenceID] = true
			}
		}
		for _, instance := range generated {
			rid := instance.Start.Property("RECURRENCE-ID").Value
			if !exceptions[instance.Event.UID][rid] {
				instances = append(instances, instance)
			}
		}

		overlappingExceptions, err := bstore.QueryTx[store.CalendarEvent](tx).
			FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
			FilterNotEqual("RecurrenceID", "").
			FilterLess("StartUTC", to).
			FilterGreater("EndUTC", from).
			List()
		if err != nil {
			return err
		}
		for _, ev := range overlappingExceptions {
			instances = append(instances, eventInstance(ev, true))
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

func overlaps(start, end, from, to time.Time) bool {
	return start.Before(to) && end.After(from)
}
