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

		exceptions := map[string]map[string]bool{}
		if len(recurring) > 0 {
			uids := make([]any, len(recurring))
			for i, ev := range recurring {
				uids[i] = ev.UID
			}
			seriesExceptions, err := bstore.QueryTx[store.CalendarEvent](tx).
				FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).
				FilterEqual("UID", uids...).
				FilterNotEqual("RecurrenceID", "").
				List()
			if err != nil {
				return err
			}
			for _, ev := range seriesExceptions {
				if exceptions[ev.UID] == nil {
					exceptions[ev.UID] = map[string]bool{}
				}
				exceptions[ev.UID][ev.RecurrenceID] = true
			}
		}

		for _, ev := range nonRecurring {
			instances = append(instances, eventInstance(ev, false))
		}
		for _, ev := range recurring {
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
				if exceptions[ev.UID][instanceStart.Property("RECURRENCE-ID").Value] {
					continue
				}
				instanceEnd := instanceStart
				instanceEnd.UTC = instanceStart.UTC.Add(duration)
				if overlaps(instanceStart.UTC, instanceEnd.UTC, from, to) {
					instances = append(instances, Instance{Event: ev, Start: instanceStart, End: instanceEnd})
				}
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

func overlaps(start, end, from, to time.Time) bool {
	return start.Before(to) && end.After(from)
}
