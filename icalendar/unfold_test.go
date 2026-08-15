package icalendar

import "testing"

func TestUnfoldFoldedLine(t *testing.T) {
	in := []byte("DESCRIPTION:This is a long\r\n  description\r\n")
	out, err := Unfold(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "DESCRIPTION:This is a long description\r\n" {
		t.Fatalf("%q", out)
	}
}

func TestUnfoldLFOnly(t *testing.T) {
	in := []byte("DESCRIPTION:This is a long\n  description\n")
	out, err := Unfold(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "DESCRIPTION:This is a long description\n" {
		t.Fatalf("%q", out)
	}
}

func TestUnfoldRejectsNUL(t *testing.T) {
	_, err := Unfold([]byte("DESCRIPTION:test\x00\r\n"))
	if err == nil {
		t.Fatal("expected error")
	}
}
