package icalendar

import (
	"bytes"
	"strings"
	"testing"
)

// auditedNonTextProperties lists every IANA property from RFC 5545, RFC 7986,
// and RFC 9073 whose default value type is not TEXT.
var auditedNonTextProperties = map[string]valueType{
	// ../rfc/5545:3.8
	"ATTACH":           vtURI,
	"ATTENDEE":         vtCalAddress,
	"COMPLETED":        vtDateTime,
	"CREATED":          vtDateTime,
	"DTEND":            vtDateTime,
	"DTSTAMP":          vtDateTime,
	"DTSTART":          vtDateTime,
	"DUE":              vtDateTime,
	"DURATION":         vtDuration,
	"EXDATE":           vtDateTime,
	"EXRULE":           vtRecur,
	"FREEBUSY":         vtPeriod,
	"GEO":              vtFloat,
	"LAST-MODIFIED":    vtDateTime,
	"ORGANIZER":        vtCalAddress,
	"PERCENT-COMPLETE": vtInteger,
	"PRIORITY":         vtInteger,
	"RDATE":            vtDateTime,
	"RECURRENCE-ID":    vtDateTime,
	"REPEAT":           vtInteger,
	"RRULE":            vtRecur,
	"SEQUENCE":         vtInteger,
	"TRIGGER":          vtDuration,
	"TZOFFSETFROM":     vtUTCOffset,
	"TZOFFSETTO":       vtUTCOffset,
	"TZURL":            vtURI,
	"URL":              vtURI,

	// ../rfc/7986:5
	"CONFERENCE":       vtURI,
	"IMAGE":            vtURI,
	"REFRESH-INTERVAL": vtDuration,
	"SOURCE":           vtURI,

	// ../rfc/9073:6.4
	"CALENDAR-ADDRESS": vtCalAddress,
}

func TestDefaultPropertyValueTypeRegistryComplete(t *testing.T) {
	for name, want := range auditedNonTextProperties {
		got, ok := defaultPropertyValueType[name]
		if !ok {
			t.Errorf("missing non-TEXT default for %s", name)
			continue
		}
		if got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
	for name := range defaultPropertyValueType {
		if _, ok := auditedNonTextProperties[name]; !ok {
			t.Errorf("unexpected non-TEXT default for %s", name)
		}
	}
}

func TestParsePropertyCalendarAddressNoUnescape(t *testing.T) {
	line := `CALENDAR-ADDRESS:mailto:alice@example.com,foo\;bar`
	p, err := ParseProperty([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	want := `mailto:alice@example.com,foo\;bar`
	if p.Value != want {
		t.Fatalf("got %q, want %q", p.Value, want)
	}
}

func TestPropertyWriteToCalendarAddressNoEscape(t *testing.T) {
	p := Property{Name: "CALENDAR-ADDRESS", Value: "mailto:alice@example.com,a,b;c\\d"}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSuffix(buf.String(), "\r\n")
	want := `CALENDAR-ADDRESS:mailto:alice@example.com,a,b;c\d`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, `\,`) || strings.Contains(got, `\;`) {
		t.Fatalf("TEXT escapes in CAL-ADDRESS value: %q", got)
	}
}
