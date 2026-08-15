package icalendar

import (
	"errors"
	"testing"
	"time"
)

func TestExpandWeeklySkipsDSTGapAndKeepsLocalHour(t *testing.T) {
	start, err := ParseDateTime(Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20260323T090000"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseRRULE("FREQ=WEEKLY;BYDAY=MO;COUNT=3")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	inst, err := Expand(start, r, nil, nil, from, to, 520)
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 3 {
		t.Fatalf("len=%d", len(inst))
	}
	// Mondays 23 Mar, 30 Mar, 6 Apr 2026 at 09:00 Europe/Berlin.
	// 29 Mar 2026 is DST start; 30 Mar is still 09:00 local (07:00 UTC).
	want := []time.Time{
		time.Date(2026, 3, 23, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 30, 7, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 6, 7, 0, 0, 0, time.UTC),
	}
	for i := range want {
		if !inst[i].UTC.Equal(want[i]) {
			t.Fatalf("inst[%d]=%s want %s", i, inst[i].UTC, want[i])
		}
		if inst[i].TZID != "Europe/Berlin" {
			t.Fatalf("tz %q", inst[i].TZID)
		}
	}
}

func TestExpandExdateRemovesOccurrence(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)} // Monday
	r, err := ParseRRULE("FREQ=WEEKLY;COUNT=3")
	if err != nil {
		t.Fatal(err)
	}
	ex := []DateTime{{UTC: time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)}}
	inst, err := Expand(start, r, nil, ex, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 520)
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 2 || !inst[0].UTC.Equal(start.UTC) || !inst[1].UTC.Equal(time.Date(2026, 1, 19, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%v", inst)
	}
}

func TestExpandMonthlyByDayLastFriday(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 30, 15, 0, 0, 0, time.UTC), TZID: "UTC"}
	r, err := ParseRRULE("FREQ=MONTHLY;BYDAY=-1FR;COUNT=2")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := Expand(start, r, nil, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 520)
	if err != nil {
		t.Fatal(err)
	}
	if len(inst) != 2 {
		t.Fatalf("len=%d %#v", len(inst), inst)
	}
}

func TestExpandMonthlyByDayFifthFriday(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 30, 15, 0, 0, 0, time.UTC), TZID: "UTC"}
	r, err := ParseRRULE("FREQ=MONTHLY;BYDAY=5FR;COUNT=2")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := Expand(start, r, nil, nil, start.UTC, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 520)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 5, 29, 15, 0, 0, 0, time.UTC)
	if len(inst) != 2 || !inst[1].UTC.Equal(want) {
		t.Fatalf("%#v", inst)
	}
}

func TestExpandHitsMax(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r, err := ParseRRULE("FREQ=DAILY")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Expand(start, r, nil, nil, start.UTC, start.UTC.Add(100*24*time.Hour), 10)
	if !errors.Is(err, ErrExpansionLimit) {
		t.Fatalf("got %v", err)
	}
}

func TestParseRRULERejectsUnsupportedParts(t *testing.T) {
	for _, value := range []string{
		"FREQ=DAILY;BYHOUR=9",
		"FREQ=MONTHLY;BYSETPOS=1",
		"FREQ=WEEKLY;WKST=SU",
		"FREQ=DAILY;COUNT=2;UNTIL=20260103T000000Z",
		"FREQ=DAILY;BYDAY=MO",
	} {
		if _, err := ParseRRULE(value); err == nil {
			t.Errorf("ParseRRULE(%q) succeeded", value)
		}
	}
}

func TestComponentRecurrenceSet(t *testing.T) {
	c := Component{Properties: []Property{
		{Name: "RRULE", Value: "FREQ=DAILY;COUNT=2"},
		{Name: "RDATE", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20260330T090000,20260331T090000"},
		{Name: "EXDATE", Params: []Param{{Name: "VALUE", Values: []string{"DATE"}}}, Value: "20260331"},
	}}
	r, rdates, exdates, err := c.RecurrenceSet()
	if err != nil {
		t.Fatal(err)
	}
	if r.Freq != "DAILY" || r.Count != 2 || len(rdates) != 2 || len(exdates) != 1 {
		t.Fatalf("r=%#v rdates=%#v exdates=%#v", r, rdates, exdates)
	}
	if rdates[0].TZID != "Europe/Berlin" || !exdates[0].DateOnly {
		t.Fatalf("rdates=%#v exdates=%#v", rdates, exdates)
	}
}

func TestExpandRDATEUnionAndDateOnlyEXDATE(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	r, err := ParseRRULE("FREQ=DAILY;COUNT=2")
	if err != nil {
		t.Fatal(err)
	}
	rdates := []DateTime{
		{UTC: start.UTC},
		{UTC: time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)},
	}
	exdates := []DateTime{{UTC: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), DateOnly: true}}
	got, err := Expand(start, r, rdates, exdates, start.UTC.Add(-time.Hour), start.UTC.Add(72*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].UTC.Equal(start.UTC) || !got[1].UTC.Equal(rdates[1].UTC) {
		t.Fatalf("%#v", got)
	}
}

func TestExpandIncludesDTSTARTWhenUntilPredatesIt(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)}
	r, err := ParseRRULE("FREQ=DAILY;UNTIL=20260104T090000Z")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Expand(start, r, nil, nil, start.UTC.Add(-time.Hour), start.UTC.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].UTC.Equal(start.UTC) {
		t.Fatalf("%#v", got)
	}
}

func TestExpandMonthlyByMonthDayAndYearly(t *testing.T) {
	start := DateTime{UTC: time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)}
	monthly, err := ParseRRULE("FREQ=MONTHLY;BYMONTHDAY=31;COUNT=3")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Expand(start, monthly, nil, nil, start.UTC, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1].UTC.Month() != time.March || got[2].UTC.Month() != time.May {
		t.Fatalf("%#v", got)
	}

	yearly, err := ParseRRULE("FREQ=YEARLY;BYMONTHDAY=30;COUNT=2")
	if err != nil {
		t.Fatal(err)
	}
	got, err = Expand(start, yearly, nil, nil, start.UTC, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 30, 9, 0, 0, 0, time.UTC)
	if len(got) != 2 || !got[1].UTC.Equal(want) {
		t.Fatalf("%#v", got)
	}
}
