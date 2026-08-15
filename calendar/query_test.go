package calendar

import (
	"testing"
	"time"

	"github.com/mjl-/mox/icalendar"
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
