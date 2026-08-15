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
	val := strings.Repeat("a", 80)
	p := Property{Name: "DESCRIPTION", Value: val}
	var buf bytes.Buffer
	if err := p.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if !bytes.Contains(out, []byte("\r\n ")) {
		t.Fatalf("expected fold, got %q", out)
	}
	for _, line := range bytes.Split(out, []byte("\r\n")) {
		line = bytes.TrimPrefix(line, []byte(" "))
		if len(line) > 75 {
			t.Fatalf("line too long (%d): %q", len(line), line)
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
