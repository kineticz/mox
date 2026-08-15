package icalendar

import (
	"testing"
)

func TestParseSerializeRoundTripUnknownProperty(t *testing.T) {
	in := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//calendar//EN\r\nBEGIN:VEVENT\r\nUID:one@example.com\r\nDTSTAMP:20260101T120000Z\r\nDTSTART:20260102T090000Z\r\nDTEND:20260102T100000Z\r\nSUMMARY:Hello\r\nX-MOX-CUSTOM:keep-me\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	cal, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Components) != 1 || cal.Components[0].Name != "VEVENT" {
		t.Fatalf("%#v", cal.Components)
	}
	p, ok := cal.Components[0].Prop("X-MOX-CUSTOM")
	if !ok || p.Value != "keep-me" {
		t.Fatalf("unknown property: %#v", p)
	}
	out := cal.Serialize()
	cal2, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	p2, ok := cal2.Components[0].Prop("X-MOX-CUSTOM")
	if !ok || p2.Value != "keep-me" {
		t.Fatalf("round-trip lost custom property: %#v", p2)
	}
	if sum, _ := cal2.Components[0].Prop("SUMMARY"); sum.Value != "Hello" {
		t.Fatalf("summary %q", sum.Value)
	}
}

func TestParseNestedVALARMRoundTrip(t *testing.T) {
	in := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//calendar//EN\r\nBEGIN:VEVENT\r\nUID:one@example.com\r\nDTSTAMP:20260101T120000Z\r\nDTSTART:20260102T090000Z\r\nDTEND:20260102T100000Z\r\nSUMMARY:Hello\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Reminder\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	cal, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	ev := cal.Components[0]
	if len(ev.Components) != 1 || ev.Components[0].Name != "VALARM" {
		t.Fatalf("VALARM subcomponent: %#v", ev.Components)
	}
	desc, ok := ev.Components[0].Prop("DESCRIPTION")
	if !ok || desc.Value != "Reminder" {
		t.Fatalf("DESCRIPTION: %#v", desc)
	}
	cal2, err := Parse(cal.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	ev2 := cal2.Components[0]
	if len(ev2.Components) != 1 || ev2.Components[0].Name != "VALARM" {
		t.Fatalf("round-trip lost VALARM: %#v", ev2.Components)
	}
}

func TestParseTopLevelVTODOPreserved(t *testing.T) {
	in := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//calendar//EN\r\nBEGIN:VTODO\r\nUID:todo@example.com\r\nDTSTAMP:20260101T120000Z\r\nSUMMARY:Task\r\nEND:VTODO\r\nEND:VCALENDAR\r\n")
	cal, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Components) != 1 || cal.Components[0].Name != "VTODO" {
		t.Fatalf("%#v", cal.Components)
	}
	cal2, err := Parse(cal.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	if len(cal2.Components) != 1 || cal2.Components[0].Name != "VTODO" {
		t.Fatalf("round-trip lost VTODO: %#v", cal2.Components)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"missing begin", "VERSION:2.0\r\nEND:VCALENDAR\r\n"},
		{"missing end", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n"},
		{"mismatched end", "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VCALENDAR\r\n"},
		{"data after end", "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\nX:1\r\n"},
		{"empty name", "BEGIN:\r\nEND:VCALENDAR\r\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse([]byte(c.in)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
