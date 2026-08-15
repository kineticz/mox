package calendar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

var testctx = context.Background()

func testAccount(t *testing.T) *store.Account {
	t.Helper()
	os.RemoveAll("../testdata/store/data")
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/store/mox.conf")
	mox.MustLoadConfig(true, false)
	mox.Conf.Calendar.EnableCalendar = true
	mox.ApplyCalendarDefaults(&mox.Conf.Calendar)
	if err := store.Init(testctx); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	t.Cleanup(store.Switchboard())

	acc, err := store.OpenAccount(mlog.New("calendar", nil), "mjl", false)
	if err != nil {
		t.Fatalf("open account: %v", err)
	}
	t.Cleanup(func() {
		if err := acc.Close(); err != nil {
			t.Errorf("close account: %v", err)
		}
		acc.WaitClosed()
	})
	return acc
}

func testTimes() (icalendar.DateTime, icalendar.DateTime) {
	return icalendar.DateTime{UTC: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)},
		icalendar.DateTime{UTC: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)}
}

func TestCreateGetUpdateStaleDelete(t *testing.T) {
	acc := testAccount(t)
	start, end := testTimes()
	svc := Service{}
	ev, err := svc.Create(testctx, acc, EventInput{
		Summary: "Meet",
		Start:   start,
		End:     end,
		Unknown: []icalendar.Property{{Name: "X-MOX-TEST", Value: "preserved"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Revision != 1 || ev.UID == "" || !strings.HasSuffix(ev.UID, "@mox") || ev.Summary != "Meet" {
		t.Fatalf("%#v", ev)
	}
	if !strings.HasPrefix(string(ev.ICS), "BEGIN:VEVENT\r\n") ||
		!strings.Contains(string(ev.ICS), "X-MOX-TEST:preserved\r\n") ||
		!strings.HasSuffix(string(ev.ICS), "END:VEVENT\r\n") {
		t.Fatalf("unexpected ICS:\n%s", ev.ICS)
	}

	got, err := svc.Get(testctx, acc, ev.ID)
	if err != nil || got.Summary != "Meet" {
		t.Fatalf("%v %#v", err, got)
	}
	_, err = svc.Update(testctx, acc, ev.ID, EventInput{Revision: 0, Summary: "Nope", Start: start, End: end})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	up, err := svc.Update(testctx, acc, ev.ID, EventInput{
		Revision:    ev.Revision,
		UID:         ev.UID,
		Summary:     "Meet 2",
		Description: "notes",
		Start:       start,
		End:         end,
	})
	if err != nil || up.Revision != 2 || up.Sequence != 1 || up.Summary != "Meet 2" {
		t.Fatalf("%#v %v", up, err)
	}
	if err := svc.Delete(testctx, acc, ev.ID, up.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(testctx, acc, ev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestCreateDisabled(t *testing.T) {
	acc := testAccount(t)
	start, end := testTimes()
	mox.Conf.Calendar.EnableCalendar = false
	defer func() { mox.Conf.Calendar.EnableCalendar = true }()

	svc := Service{}
	if _, err := svc.Create(testctx, acc, EventInput{Summary: "x", Start: start, End: end}); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Get(testctx, acc, 1); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("get: %v", err)
	}
	if _, err := svc.Update(testctx, acc, 1, EventInput{}); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("update: %v", err)
	}
	if err := svc.Delete(testctx, acc, 1, 1, false); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("delete: %v", err)
	}
}

func TestCreateValidationAndLimits(t *testing.T) {
	acc := testAccount(t)
	start, end := testTimes()
	svc := Service{}

	tests := []struct {
		name string
		in   EventInput
		err  error
	}{
		{
			name: "end before start",
			in:   EventInput{Start: end, End: start},
			err:  ErrInvalid,
		},
		{
			name: "date end equals start",
			in: EventInput{
				Start: icalendar.DateTime{UTC: start.UTC, DateOnly: true},
				End:   icalendar.DateTime{UTC: start.UTC, DateOnly: true},
			},
		},
		{
			name: "date end before start",
			in: EventInput{
				Start: icalendar.DateTime{UTC: start.UTC, DateOnly: true},
				End:   icalendar.DateTime{UTC: start.UTC.AddDate(0, 0, -1), DateOnly: true},
			},
			err: ErrInvalid,
		},
		{
			name: "invalid rrule",
			in:   EventInput{Start: start, End: end, RRULE: "FREQ=HOURLY"},
			err:  ErrInvalid,
		},
		{
			name: "this and future",
			in: EventInput{Start: start, End: end, Unknown: []icalendar.Property{{
				Name:   "RECURRENCE-ID",
				Params: []icalendar.Param{{Name: "RANGE", Values: []string{"THISANDFUTURE"}}},
				Value:  "20260815T090000Z",
			}}},
			err: ErrInvalid,
		},
		{
			name: "vtodo injection",
			in: EventInput{Start: start, End: end, Unknown: []icalendar.Property{{
				Name: "BEGIN", Value: "VTODO",
			}}},
			err: ErrInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(testctx, acc, tc.in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, expected %v", err, tc.err)
			}
		})
	}

	mox.Conf.Calendar.MaxAttendees = 1
	_, err := svc.Create(testctx, acc, EventInput{
		Summary: "x", Start: start, End: end,
		Attendees: []store.CalendarAttendee{{Address: "a@e.com"}, {Address: "b@e.com"}},
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("attendees: %v", err)
	}
	mox.Conf.Calendar.MaxAttendees = 64

	mox.Conf.Calendar.MaxICSBytes = 80
	_, err = svc.Create(testctx, acc, EventInput{
		Summary: strings.Repeat("x", 100), Start: start, End: end,
	})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("ICS bytes: %v", err)
	}
}

func TestExceptionCreationAndLimit(t *testing.T) {
	acc := testAccount(t)
	start, end := testTimes()
	svc := Service{}

	if _, err := svc.Create(testctx, acc, EventInput{
		UID: "missing@mox", RecurrenceID: "20260816T090000Z", Start: start, End: end,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("exception without master: %v", err)
	}

	master, err := svc.Create(testctx, acc, EventInput{
		UID: "series@mox", Start: start, End: end, RRULE: "FREQ=DAILY;COUNT=4",
		EXDATE: []string{"20260818T090000Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(testctx, acc, EventInput{
		UID: master.UID, RecurrenceID: "20260816T090000Z",
		Start: icalendar.DateTime{UTC: start.UTC.AddDate(0, 0, 1)},
		End:   icalendar.DateTime{UTC: end.UTC.AddDate(0, 0, 1)},
	}); err != nil {
		t.Fatal(err)
	}

	mox.Conf.Calendar.MaxExceptions = 2
	if _, err := svc.Create(testctx, acc, EventInput{
		UID: master.UID, RecurrenceID: "20260817T090000Z",
		Start: icalendar.DateTime{UTC: start.UTC.AddDate(0, 0, 2)},
		End:   icalendar.DateTime{UTC: end.UTC.AddDate(0, 0, 2)},
	}); !errors.Is(err, ErrLimit) {
		t.Fatalf("exception limit: %v", err)
	}
}

func TestDeleteSeriesSemantics(t *testing.T) {
	acc := testAccount(t)
	start, end := testTimes()
	svc := Service{}

	createSeries := func(t *testing.T, uid string) (store.CalendarEvent, store.CalendarEvent) {
		t.Helper()
		master, err := svc.Create(testctx, acc, EventInput{
			UID: uid, Start: start, End: end, RRULE: "FREQ=DAILY;COUNT=2",
		})
		if err != nil {
			t.Fatal(err)
		}
		exception, err := svc.Create(testctx, acc, EventInput{
			UID: uid, RecurrenceID: "20260816T090000Z",
			Start: icalendar.DateTime{UTC: start.UTC.AddDate(0, 0, 1)},
			End:   icalendar.DateTime{UTC: end.UTC.AddDate(0, 0, 1)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return master, exception
	}

	master, exception := createSeries(t, "series-delete-master@mox")
	if err := svc.Delete(testctx, acc, master.ID, master.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(testctx, acc, master.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("master still exists: %v", err)
	}
	if _, err := svc.Get(testctx, acc, exception.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exception still exists: %v", err)
	}

	master, exception = createSeries(t, "series-delete-exception@mox")
	if err := svc.Delete(testctx, acc, exception.ID, exception.Revision, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(testctx, acc, master.ID); err != nil {
		t.Fatalf("master deleted with exception: %v", err)
	}
	if _, err := svc.Get(testctx, acc, exception.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exception still exists: %v", err)
	}

	master, exception = createSeries(t, "series-delete-whole@mox")
	if err := svc.Delete(testctx, acc, exception.ID, exception.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(testctx, acc, master.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("master still exists after whole-series delete: %v", err)
	}
}

func TestDeleteStaleAndNoOutbox(t *testing.T) {
	acc := testAccount(t)
	start, end := testTimes()
	ev, err := (Service{}).Create(testctx, acc, EventInput{Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
	if err := (Service{}).Delete(testctx, acc, ev.ID, ev.Revision+1, false); !errors.Is(err, ErrStale) {
		t.Fatalf("stale delete: %v", err)
	}

	var outgoing int
	if err := acc.DB.Read(testctx, func(tx *bstore.Tx) error {
		var err error
		outgoing, err = bstore.QueryTx[store.Outgoing](tx).Count()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if outgoing != 0 {
		t.Fatalf("calendar mutation wrote %d outbox rows", outgoing)
	}
}
