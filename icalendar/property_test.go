package icalendar

import (
	"bytes"
	"strings"
	"testing"
)

func TestParsePropertyParamsAndEscapes(t *testing.T) {
	p, err := ParseProperty([]byte(`ATTENDEE;CN="Doe, Jane";RSVP=TRUE:mailto:jane@example.com`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "ATTENDEE" || p.Value != "mailto:jane@example.com" {
		t.Fatalf("%#v", p)
	}
	cn, ok := p.Param("cn")
	if !ok || cn != "Doe, Jane" {
		t.Fatalf("CN: %q %v", cn, ok)
	}
}

func TestParsePropertyUnknownName(t *testing.T) {
	p, err := ParseProperty([]byte(`X-CUSTOM-PROP:hello`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "X-CUSTOM-PROP" || p.Value != "hello" {
		t.Fatalf("%#v", p)
	}
}

func TestParsePropertyValueUnescape(t *testing.T) {
	p, err := ParseProperty([]byte(`DESCRIPTION:line1\\nline2\;part\,two`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Value != "line1\\nline2;part,two" {
		t.Fatalf("%q", p.Value)
	}
}

func TestEscapeUnescapeText(t *testing.T) {
	in := "a\\b;c\nd,e"
	got := UnescapeText(EscapeText(in))
	if got != in {
		t.Fatalf("got %q", got)
	}
}

func TestPropertyWriteToFold(t *testing.T) {
	val := strings.Repeat("a", 140)
	p := Property{Name: "DESCRIPTION", Value: val}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if !bytes.Contains(out, []byte("\r\n ")) {
		t.Fatalf("expected fold, got %q", out)
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(out, []byte("\r\n")), []byte("\r\n")) {
		if len(line) > 75 {
			t.Fatalf("physical line too long (%d): %q", len(line), line)
		}
	}
	unfolded, err := Unfold(out)
	if err != nil {
		t.Fatal(err)
	}
	unfolded = bytes.TrimSuffix(unfolded, []byte("\r\n"))
	p2, err := ParseProperty(unfolded)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Name != p.Name || p2.Value != p.Value {
		t.Fatalf("got %#v", p2)
	}
}

func TestPropertyWriteToEscapesTEXT(t *testing.T) {
	p := Property{Name: "DESCRIPTION", Value: "a;b"}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `a\;b`) {
		t.Fatalf("%q", buf.String())
	}
}

func TestPropertyWriteToNoEscapeCalAddress(t *testing.T) {
	p := Property{Name: "ATTENDEE", Value: "mailto:jane@example.com"}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `\`) {
		t.Fatalf("%q", buf.String())
	}
}

func TestParsePropertyRRULENoUnescape(t *testing.T) {
	in := `RRULE:FREQ=WEEKLY;BYDAY=MO,TU`
	p, err := ParseProperty([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if p.Value != "FREQ=WEEKLY;BYDAY=MO,TU" {
		t.Fatalf("got %q", p.Value)
	}
}

func TestPropertyWriteToRRULENoEscape(t *testing.T) {
	p := Property{Name: "RRULE", Value: "FREQ=WEEKLY;BYDAY=MO,TU"}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSuffix(buf.String(), "\r\n")
	if got != "RRULE:FREQ=WEEKLY;BYDAY=MO,TU" {
		t.Fatalf("got %q", got)
	}
}

func TestParsePropertyMultiValueParam(t *testing.T) {
	p, err := ParseProperty([]byte(`ATTENDEE;ROLE=CHAIR,REQ-PARTICIPANT:mailto:host@example.com`))
	if err != nil {
		t.Fatal(err)
	}
	for _, param := range p.Params {
		if param.Name == "ROLE" {
			if len(param.Values) != 2 || param.Values[0] != "CHAIR" || param.Values[1] != "REQ-PARTICIPANT" {
				t.Fatalf("Values: %#v", param.Values)
			}
			return
		}
	}
	t.Fatal("missing ROLE param")
}

func TestParsePropertyMultiValueQuotedParam(t *testing.T) {
	p, err := ParseProperty([]byte(`ATTENDEE;MEMBER="mailto:a@example.com","mailto:b@example.com":mailto:host@example.com`))
	if err != nil {
		t.Fatal(err)
	}
	for _, param := range p.Params {
		if param.Name == "MEMBER" {
			if len(param.Values) != 2 || param.Values[0] != "mailto:a@example.com" || param.Values[1] != "mailto:b@example.com" {
				t.Fatalf("Values: %#v", param.Values)
			}
			return
		}
	}
	t.Fatal("missing MEMBER param")
}

func TestPropertyWriteToMultiValueParam(t *testing.T) {
	p := Property{
		Name: "ATTENDEE",
		Params: []Param{{
			Name:   "ROLE",
			Values: []string{"CHAIR", "REQ-PARTICIPANT"},
		}},
		Value: "mailto:host@example.com",
	}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSuffix(buf.String(), "\r\n")
	want := "ATTENDEE;ROLE=CHAIR,REQ-PARTICIPANT:mailto:host@example.com"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestParsePropertyQuotedParamBackslash(t *testing.T) {
	p, err := ParseProperty([]byte(`ATTENDEE;CN="A\B":mailto:x@example.com`))
	if err != nil {
		t.Fatal(err)
	}
	cn, ok := p.Param("CN")
	if !ok || cn != `A\B` {
		t.Fatalf("CN: %q %v", cn, ok)
	}
}

func TestPropertyWriteToQuotedParamBackslash(t *testing.T) {
	p := Property{
		Name:   "ATTENDEE",
		Params: []Param{{Name: "CN", Values: []string{`A\B`}}},
		Value:  "mailto:x@example.com",
	}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSuffix(buf.String(), "\r\n")
	want := `ATTENDEE;CN=A\B:mailto:x@example.com`
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestParsePropertyInvalidToken(t *testing.T) {
	cases := []string{
		`BAD NAME:value`,
		`PROP;;P2=V:value`,
		`PROP;=V:value`,
	}
	for _, c := range cases {
		if _, err := ParseProperty([]byte(c)); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}
