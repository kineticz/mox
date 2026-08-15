package icalendar

import (
	"testing"
	"time"
)

func TestSplitSeriesRejectsUIDlessVEVENT(t *testing.T) {
	cases := []struct {
		name string
		ics  string
	}{
		{
			name: "master",
			ics:  "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260105T090000Z\r\nSUMMARY:No UID\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
		{
			name: "exception",
			ics:  "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260112T110000Z\r\nRECURRENCE-ID:20260112T090000Z\r\nSUMMARY:No UID\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cal, err := Parse([]byte(c.ics))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = SplitSeries(cal.Components)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestSplitSeriesMasterAndException(t *testing.T) {
	cal, err := Parse([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:s@example.com\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260105T090000Z\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:Series\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:s@example.com\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260112T110000Z\r\nRECURRENCE-ID:20260112T090000Z\r\nSUMMARY:Moved\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	masters, ex, err := SplitSeries(cal.Components)
	if err != nil {
		t.Fatal(err)
	}
	if len(masters) != 1 || len(ex["s@example.com"]) != 1 {
		t.Fatalf("masters=%d ex=%d", len(masters), len(ex["s@example.com"]))
	}
	rid, ok, err := ex["s@example.com"][0].RecurrenceID()
	if err != nil || !ok || !rid.UTC.Equal(time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v %v %v", rid, ok, err)
	}
}
