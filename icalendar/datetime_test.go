package icalendar

import (
	"testing"
	"time"
)

func TestParseDateTimeUTC(t *testing.T) {
	p := Property{Name: "DTSTART", Value: "20260815T090000Z"}
	dt, err := ParseDateTime(p)
	if err != nil {
		t.Fatal(err)
	}
	if dt.DateOnly || dt.TZID != "" || dt.UTC.UTC() != time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC) {
		t.Fatalf("%#v", dt)
	}
}

func TestParseDateTimeBerlinDST(t *testing.T) {
	p := Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20260329T090000"}
	dt, err := ParseDateTime(p)
	if err != nil {
		t.Fatal(err)
	}
	// Europe/Berlin 2026-03-29 09:00 is CEST (UTC+2).
	if dt.TZID != "Europe/Berlin" || dt.UTC.UTC() != time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC) {
		t.Fatalf("%#v %s", dt, dt.UTC)
	}
}

func TestParseDateTimeBerlinOverlapUsesFirstOccurrence(t *testing.T) {
	p := Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20261025T023000"}
	dt, err := ParseDateTime(p)
	if err != nil {
		t.Fatal(err)
	}
	// RFC 5545 section 3.3.5 requires the first occurrence, still at UTC+2.
	want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	if !dt.UTC.Equal(want) {
		t.Fatalf("UTC = %s, want %s", dt.UTC, want)
	}
}

func TestParseDateTimeBerlinGapUsesOffsetBeforeGap(t *testing.T) {
	p := Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20260329T023000"}
	dt, err := ParseDateTime(p)
	if err != nil {
		t.Fatal(err)
	}
	// RFC 5545 section 3.3.5 requires the UTC+1 offset from before the gap.
	want := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)
	if !dt.UTC.Equal(want) {
		t.Fatalf("UTC = %s, want %s", dt.UTC, want)
	}
}

func TestParseDateOnly(t *testing.T) {
	p := Property{Name: "DTSTART", Params: []Param{{Name: "VALUE", Values: []string{"DATE"}}}, Value: "20260101"}
	dt, err := ParseDateTime(p)
	if err != nil {
		t.Fatal(err)
	}
	if !dt.DateOnly || !dt.UTC.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%#v", dt)
	}
}

func TestDateTimeSerializePreservesTZID(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	d := DateTime{UTC: time.Date(2026, 1, 12, 8, 0, 0, 0, time.UTC), TZID: "Europe/Berlin"}
	p := d.Property("DTSTART")
	tz, _ := p.Param("TZID")
	if tz != "Europe/Berlin" || p.Value != "20260112T090000" {
		t.Fatalf("%#v", p)
	}
	_ = loc
}

func TestParseDateTimeFloatingRoundTrip(t *testing.T) {
	dt, err := ParseDateTime(Property{Name: "DTSTART", Value: "20260815T090000"})
	if err != nil {
		t.Fatal(err)
	}
	if !dt.Floating || !dt.UTC.Equal(time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%#v", dt)
	}
	p := dt.Property("DTSTART")
	if len(p.Params) != 0 || p.Value != "20260815T090000" {
		t.Fatalf("%#v", p)
	}
}

func TestParseDateTimeUnknownTZID(t *testing.T) {
	p := Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Unknown/Zone"}}}, Value: "20260815T090000"}
	if _, err := ParseDateTime(p); err == nil {
		t.Fatal("expected error")
	}
}

func TestDateTimeSerializeDateAndUTC(t *testing.T) {
	date := DateTime{UTC: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DateOnly: true}.Property("DTSTART")
	value, ok := date.Param("VALUE")
	if !ok || value != "DATE" || date.Value != "20260101" {
		t.Fatalf("date: %#v", date)
	}

	instant := DateTime{UTC: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)}.Property("DTSTART")
	if len(instant.Params) != 0 || instant.Value != "20260815T090000Z" {
		t.Fatalf("instant: %#v", instant)
	}
}
