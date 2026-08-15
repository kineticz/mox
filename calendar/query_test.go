package calendar

import (
	"errors"
	"testing"
	"time"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mox-"
)

func TestQueryWindowIncludesRecurringInstance(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	_, err := svc.Create(ctx, acc, EventInput{
		Summary: "Standup",
		Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
		End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)},
		RRULE:   "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := svc.Query(ctx, acc, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 1 || inst[0].Start.UTC != time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC) {
		t.Fatalf("%#v", inst)
	}
}

func TestQueryExceptionReplacesOccurrence(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	master, err := svc.Create(ctx, acc, EventInput{
		Summary: "Standup",
		Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
		End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)},
		RRULE:   "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, acc, EventInput{
		UID:          master.UID,
		RecurrenceID: "20260112T090000Z",
		Summary:      "Moved standup",
		Start:        icalendar.DateTime{UTC: time.Date(2026, 1, 12, 11, 0, 0, 0, time.UTC)},
		End:          icalendar.DateTime{UTC: time.Date(2026, 1, 12, 11, 15, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := svc.Query(ctx, acc, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 1 || inst[0].Start.UTC != time.Date(2026, 1, 12, 11, 0, 0, 0, time.UTC) || !inst[0].Exception {
		t.Fatalf("%#v", inst)
	}
}

func TestQueryExceptionMovedOutSuppressesOccurrence(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	master, err := svc.Create(ctx, acc, EventInput{
		Summary: "Standup",
		Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
		End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)},
		RRULE:   "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, acc, EventInput{
		UID:          master.UID,
		RecurrenceID: "20260112T090000Z",
		Summary:      "Moved standup",
		Start:        icalendar.DateTime{UTC: time.Date(2026, 1, 14, 11, 0, 0, 0, time.UTC)},
		End:          icalendar.DateTime{UTC: time.Date(2026, 1, 14, 11, 15, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := svc.Query(ctx, acc, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 0 {
		t.Fatalf("%#v", inst)
	}
}

func TestQueryDateOnlyZeroDurationOccupiesDay(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	day := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	_, err := svc.Create(ctx, acc, EventInput{
		Summary: "All day",
		Start:   icalendar.DateTime{UTC: day, DateOnly: true},
		End:     icalendar.DateTime{UTC: day, DateOnly: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := svc.Query(ctx, acc, day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 1 || !inst[0].Start.UTC.Equal(day) || !inst[0].Start.DateOnly {
		t.Fatalf("%#v", inst)
	}
}

func TestQueryDateOnlyRecurringZeroDurationInstance(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	day := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	_, err := svc.Create(ctx, acc, EventInput{
		Summary: "All-day series",
		Start:   icalendar.DateTime{UTC: day, DateOnly: true},
		End:     icalendar.DateTime{UTC: day, DateOnly: true},
		RRULE:   "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatal(err)
	}
	from := day.AddDate(0, 0, 1)
	inst, err := svc.Query(ctx, acc, from, from.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 1 || !inst[0].Start.UTC.Equal(from) || !inst[0].Start.DateOnly {
		t.Fatalf("%#v", inst)
	}
}

func TestQueryRDATEOnlyExpands(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	_, err := svc.Create(ctx, acc, EventInput{
		Summary: "Extra date",
		Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
		End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)},
		RDATE:   []string{"20260112T090000Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := svc.Query(ctx, acc, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 1 || !inst[0].Start.UTC.Equal(time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%#v", inst)
	}
}

func TestQueryExpansionLimitIsErrLimit(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	_, err := svc.Create(ctx, acc, EventInput{
		Summary: "Daily",
		Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
		End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)},
		RRULE:   "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatal(err)
	}
	orig := mox.Conf.Calendar.MaxRecurrenceInstances
	mox.Conf.Calendar.MaxRecurrenceInstances = 2
	t.Cleanup(func() { mox.Conf.Calendar.MaxRecurrenceInstances = orig })

	_, err = svc.Query(ctx, acc, time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v, expected %v", err, ErrLimit)
	}
}

func TestQueryTZIDExceptionMatchesUTCRecurrenceID(t *testing.T) {
	acc := testAccount(t)
	ctx := testctx
	svc := Service{}
	master, err := svc.Create(ctx, acc, EventInput{
		Summary: "Standup",
		Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC), TZID: "Europe/Berlin"},
		End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC), TZID: "Europe/Berlin"},
		RRULE:   "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(ctx, acc, EventInput{
		UID:          master.UID,
		RecurrenceID: "20260112T090000Z",
		Summary:      "Moved standup",
		Start:        icalendar.DateTime{UTC: time.Date(2026, 1, 12, 11, 0, 0, 0, time.UTC), TZID: "Europe/Berlin"},
		End:          icalendar.DateTime{UTC: time.Date(2026, 1, 12, 11, 15, 0, 0, time.UTC), TZID: "Europe/Berlin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := svc.Query(ctx, acc, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 1 || !inst[0].Start.UTC.Equal(time.Date(2026, 1, 12, 11, 0, 0, 0, time.UTC)) || !inst[0].Exception {
		t.Fatalf("%#v", inst)
	}
}
