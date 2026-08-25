package calendar

import (
	"errors"
	"strconv"
	"testing"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

func enableIMIP(t *testing.T) {
	t.Helper()
	mox.Conf.Calendar.EnableIMIP = true
	t.Cleanup(func() { mox.Conf.Calendar.EnableIMIP = false })
}

func parseCal(t *testing.T, s string) icalendar.Calendar {
	t.Helper()
	cal, err := icalendar.Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse calendar: %v", err)
	}
	return cal
}

func inboundHas(t *testing.T, acc *store.Account, method, uid string, seq int, result string) {
	t.Helper()
	var n int
	err := acc.DB.Read(testctx, func(tx *bstore.Tx) error {
		var err error
		n, err = bstore.QueryTx[store.CalendarInbound](tx).
			FilterEqual("Method", method).
			FilterEqual("UID", uid).
			FilterEqual("Sequence", seq).
			FilterEqual("Result", result).
			Count()
		return err
	})
	if err != nil || n < 1 {
		t.Fatalf("inbound %s %s seq=%d result=%s: n=%d err=%v", method, uid, seq, result, n, err)
	}
}

func inboundCount(t *testing.T, acc *store.Account) int {
	t.Helper()
	var n int
	err := acc.DB.Read(testctx, func(tx *bstore.Tx) error {
		var err error
		n, err = bstore.QueryTx[store.CalendarInbound](tx).Count()
		return err
	})
	if err != nil {
		t.Fatalf("inbound count: %v", err)
	}
	return n
}

func TestApplyIncoming(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	orgMeta := IncomingMeta{MailFrom: "org@mox.example"}

	req := func(uid string, seq int) icalendar.Calendar {
		return parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:"+uid+"\r\nSEQUENCE:"+strconv.Itoa(seq)+"\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Invite\r\nORGANIZER:mailto:org@mox.example\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:mjl@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	}

	result, err := ApplyIncoming(testctx, acc, req("imip1@mox", 1), orgMeta)
	if err != nil || result != "applied" {
		t.Fatalf("create REQUEST: result=%q err=%v", result, err)
	}
	if _, err := svc.GetObject(testctx, acc, "imip1@mox"); err != nil {
		t.Fatalf("created object: %v", err)
	}
	inboundHas(t, acc, "REQUEST", "imip1@mox", 1, "applied")
	ev := eventByUID(t, acc, "imip1@mox")
	if ev.Sequence != 1 || ev.Summary != "Invite" || ev.Organizer != "mailto:org@mox.example" {
		t.Fatalf("created event: %#v", ev)
	}

	result, err = ApplyIncoming(testctx, acc, req("imip1@mox", 1), orgMeta)
	if err != nil || result != "ignored-duplicate" {
		t.Fatalf("duplicate REQUEST: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REQUEST", "imip1@mox", 1, "ignored-duplicate")

	result, err = ApplyIncoming(testctx, acc, req("imip1@mox", 0), orgMeta)
	if err != nil || result != "ignored-stale" {
		t.Fatalf("stale REQUEST: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REQUEST", "imip1@mox", 0, "ignored-stale")
	if eventByUID(t, acc, "imip1@mox").Sequence != 1 {
		t.Fatalf("stale REQUEST replaced event")
	}

	result, err = ApplyIncoming(testctx, acc, req("imip-bad@mox", 1), IncomingMeta{MailFrom: "evil@example.com"})
	if err != nil || result != "ignored-mismatch" {
		t.Fatalf("mismatched REQUEST: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REQUEST", "imip-bad@mox", 1, "ignored-mismatch")
	if _, err := svc.GetObject(testctx, acc, "imip-bad@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatched REQUEST wrote object: %v", err)
	}

	replyICS := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:imip-reply@mox\r\nSEQUENCE:0\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Owned\r\nORGANIZER:mailto:mjl@mox.example\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:att@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, err := svc.PutObject(testctx, acc, "imip-reply@mox", replyICS, ""); err != nil {
		t.Fatal(err)
	}
	reply := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:imip-reply@mox\r\nSEQUENCE:0\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nORGANIZER:mailto:mjl@mox.example\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:att@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err = ApplyIncoming(testctx, acc, reply, IncomingMeta{MailFrom: "att@example.com"})
	if err != nil || result != "applied" {
		t.Fatalf("REPLY: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REPLY", "imip-reply@mox", 0, "applied")
	replied := eventByUID(t, acc, "imip-reply@mox")
	if replied.Sequence != 0 {
		t.Fatalf("REPLY bumped SEQUENCE to %d", replied.Sequence)
	}
	if len(replied.Attendees) != 1 || replied.Attendees[0].PartStat != "ACCEPTED" {
		t.Fatalf("REPLY partstat: %#v", replied.Attendees)
	}
	if replied.Summary != "Owned" {
		t.Fatalf("REPLY changed summary: %q", replied.Summary)
	}

	cancel := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:imip1@mox\r\nSEQUENCE:1\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nORGANIZER:mailto:org@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err = ApplyIncoming(testctx, acc, cancel, orgMeta)
	if err != nil || result != "applied" {
		t.Fatalf("CANCEL: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "CANCEL", "imip1@mox", 1, "applied")
	if _, err := svc.GetObject(testctx, acc, "imip1@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CANCEL left object: %v", err)
	}

	taf := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:imip-taf@mox\r\nSEQUENCE:1\r\nDTSTART:20260822T090000Z\r\nDTEND:20260822T100000Z\r\nRECURRENCE-ID;RANGE=THISANDFUTURE:20260822T090000Z\r\nSUMMARY:Future\r\nORGANIZER:mailto:org@mox.example\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:mjl@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err = ApplyIncoming(testctx, acc, taf, orgMeta)
	if err != nil || result != "conflict" {
		t.Fatalf("THISANDFUTURE REQUEST: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REQUEST", "imip-taf@mox", 1, "conflict")
	if _, err := svc.GetObject(testctx, acc, "imip-taf@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("THISANDFUTURE REQUEST wrote object: %v", err)
	}
}

func TestApplyIncomingDisabled(t *testing.T) {
	acc := testAccount(t)
	cal := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:imip-off@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nORGANIZER:mailto:org@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err := ApplyIncoming(testctx, acc, cal, IncomingMeta{MailFrom: "org@mox.example"})
	if err != nil || result != "ignored-mismatch" {
		t.Fatalf("disabled: result=%q err=%v", result, err)
	}
	if inboundCount(t, acc) != 0 {
		t.Fatalf("disabled wrote inbound")
	}
	if _, err := (Service{}).GetObject(testctx, acc, "imip-off@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled wrote object: %v", err)
	}
}
