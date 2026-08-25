package calendar

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mlog"
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

func outboxRows(t *testing.T, acc *store.Account) []store.CalendarOutbox {
	t.Helper()
	var rows []store.CalendarOutbox
	err := acc.DB.Read(testctx, func(tx *bstore.Tx) error {
		var err error
		rows, err = bstore.QueryTx[store.CalendarOutbox](tx).List()
		return err
	})
	if err != nil {
		t.Fatalf("outbox list: %v", err)
	}
	return rows
}

func outboxByMethod(t *testing.T, acc *store.Account, method string) []store.CalendarOutbox {
	t.Helper()
	var rows []store.CalendarOutbox
	for _, row := range outboxRows(t, acc) {
		if row.Method == method {
			rows = append(rows, row)
		}
	}
	return rows
}

func inviteInput(uid, organizer, attendee string) EventInput {
	start, end := testTimes()
	return EventInput{
		UID:       uid,
		Summary:   "Meet",
		Organizer: organizer,
		Start:     start,
		End:       end,
		Attendees: []store.CalendarAttendee{{Address: attendee, PartStat: "NEEDS-ACTION"}},
	}
}

func TestEnqueueOutboxCreateRespondDelete(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	in := inviteInput("outbox1@mox", "mailto:org@mox.example", "mailto:att@example.com")

	ev, err := svc.Create(testctx, acc, in)
	if err != nil {
		t.Fatal(err)
	}
	reqs := outboxByMethod(t, acc, "REQUEST")
	if len(reqs) != 1 {
		t.Fatalf("after Create: REQUEST rows=%d, want 1; all=%#v", len(reqs), outboxRows(t, acc))
	}
	row := reqs[0]
	if row.UID != ev.UID || row.Sequence != ev.Sequence || row.Queued {
		t.Fatalf("REQUEST row: %#v", row)
	}
	if row.Recipient != "mailto:att@example.com" {
		t.Fatalf("REQUEST recipient %q", row.Recipient)
	}
	wantKey := "REQUEST|" + ev.UID + "|0|mailto:att@example.com"
	if row.IdempotencyKey != wantKey {
		t.Fatalf("IdempotencyKey %q, want %q", row.IdempotencyKey, wantKey)
	}
	cal, err := icalendar.Parse(row.ICS)
	if err != nil || cal.Method() != "REQUEST" {
		t.Fatalf("REQUEST ICS method=%q err=%v\n%s", cal.Method(), err, row.ICS)
	}
	if _, ok := cal.Prop("PRODID"); !ok {
		t.Fatalf("REQUEST ICS missing PRODID:\n%s", row.ICS)
	}

	ev, err = svc.Respond(testctx, acc, ev.ID, ev.Revision, "mailto:att@example.com", "ACCEPTED")
	if err != nil {
		t.Fatal(err)
	}
	replies := outboxByMethod(t, acc, "REPLY")
	if len(replies) != 1 {
		t.Fatalf("after Respond: REPLY rows=%d, want 1; all=%#v", len(replies), outboxRows(t, acc))
	}
	reply := replies[0]
	if reply.Recipient != "mailto:org@mox.example" || reply.Queued || reply.Sequence != ev.Sequence {
		t.Fatalf("REPLY row: %#v", reply)
	}
	if reply.IdempotencyKey != "REPLY|"+ev.UID+"|0|mailto:org@mox.example" {
		t.Fatalf("REPLY IdempotencyKey %q", reply.IdempotencyKey)
	}
	cal, err = icalendar.Parse(reply.ICS)
	if err != nil || cal.Method() != "REPLY" {
		t.Fatalf("REPLY ICS method=%q err=%v\n%s", cal.Method(), err, reply.ICS)
	}

	if err := svc.Delete(testctx, acc, ev.ID, ev.Revision, true); err != nil {
		t.Fatal(err)
	}
	cancels := outboxByMethod(t, acc, "CANCEL")
	if len(cancels) != 1 {
		t.Fatalf("after Delete: CANCEL rows=%d, want 1; all=%#v", len(cancels), outboxRows(t, acc))
	}
	cancel := cancels[0]
	if cancel.Recipient != "mailto:att@example.com" || cancel.Queued {
		t.Fatalf("CANCEL row: %#v", cancel)
	}
	if cancel.IdempotencyKey != "CANCEL|"+ev.UID+"|0|mailto:att@example.com" {
		t.Fatalf("CANCEL IdempotencyKey %q", cancel.IdempotencyKey)
	}
	cal, err = icalendar.Parse(cancel.ICS)
	if err != nil || cal.Method() != "CANCEL" {
		t.Fatalf("CANCEL ICS method=%q err=%v\n%s", cal.Method(), err, cancel.ICS)
	}
}

func TestApplyIncomingRequestDoesNotEnqueue(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	cal := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:imip-no-outbox@mox\r\nSEQUENCE:1\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Invite\r\nORGANIZER:mailto:org@mox.example\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:mjl@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err := ApplyIncoming(testctx, acc, cal, IncomingMeta{MailFrom: "org@mox.example"})
	if err != nil || result != "applied" {
		t.Fatalf("REQUEST: result=%q err=%v", result, err)
	}
	if _, err := (Service{}).GetObject(testctx, acc, "imip-no-outbox@mox"); err != nil {
		t.Fatalf("created object: %v", err)
	}
	if rows := outboxRows(t, acc); len(rows) != 0 {
		t.Fatalf("ApplyIncoming REQUEST enqueued %#v", rows)
	}
}

func TestEnqueueOutboxDisabled(t *testing.T) {
	acc := testAccount(t)
	svc := Service{}
	ev, err := svc.Create(testctx, acc, inviteInput("outbox-off@mox", "mailto:org@mox.example", "mailto:att@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Respond(testctx, acc, ev.ID, ev.Revision, "mailto:att@example.com", "ACCEPTED"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(testctx, acc, ev.ID, ev.Revision+1, true); err != nil {
		t.Fatal(err)
	}
	if rows := outboxRows(t, acc); len(rows) != 0 {
		t.Fatalf("IMIP off wrote %#v", rows)
	}
}

func TestEnqueueOutboxExceptionSkipped(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	start, end := testTimes()
	master, err := svc.Create(testctx, acc, EventInput{
		UID:       "outbox-ex@mox",
		Summary:   "Series",
		Organizer: "mailto:org@mox.example",
		Start:     start,
		End:       end,
		RRULE:     "FREQ=DAILY;COUNT=3",
		Attendees: []store.CalendarAttendee{{Address: "mailto:att@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(outboxByMethod(t, acc, "REQUEST")); n != 1 {
		t.Fatalf("master REQUEST rows=%d", n)
	}
	exStart := icalendar.DateTime{UTC: start.UTC.AddDate(0, 0, 1)}
	exEnd := icalendar.DateTime{UTC: end.UTC.AddDate(0, 0, 1)}
	ex, err := svc.Create(testctx, acc, EventInput{
		UID:          master.UID,
		RecurrenceID: "20260816T090000Z",
		Summary:      "Exception",
		Organizer:    "mailto:org@mox.example",
		Start:        exStart,
		End:          exEnd,
		Attendees:    []store.CalendarAttendee{{Address: "mailto:att@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(outboxByMethod(t, acc, "REQUEST")); n != 1 {
		t.Fatalf("exception Create enqueued REQUEST, rows=%d", n)
	}
	if _, err := svc.Update(testctx, acc, ex.ID, EventInput{
		UID:          ex.UID,
		RecurrenceID: ex.RecurrenceID,
		Revision:     ex.Revision,
		Summary:      "Exception 2",
		Organizer:    "mailto:org@mox.example",
		Start:        exStart,
		End:          exEnd,
		Attendees:    []store.CalendarAttendee{{Address: "mailto:att@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxByMethod(t, acc, "REQUEST")); n != 1 {
		t.Fatalf("exception Update enqueued REQUEST, rows=%d", n)
	}
	if err := svc.Delete(testctx, acc, ex.ID, ex.Revision+1, false); err != nil {
		t.Fatal(err)
	}
	if n := len(outboxByMethod(t, acc, "CANCEL")); n != 0 {
		t.Fatalf("exception Delete enqueued CANCEL, rows=%d", n)
	}
}

func TestEnqueueOutboxUpdateSequence(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	start, end := testTimes()
	ev, err := svc.Create(testctx, acc, inviteInput("outbox-upd@mox", "mailto:org@mox.example", "mailto:att@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	up, err := svc.Update(testctx, acc, ev.ID, EventInput{
		UID:       ev.UID,
		Revision:  ev.Revision,
		Summary:   "Meet 2",
		Organizer: "mailto:org@mox.example",
		Start:     start,
		End:       end,
		Attendees: []store.CalendarAttendee{{Address: "mailto:att@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Sequence != ev.Sequence+1 {
		t.Fatalf("sequence %d, want %d", up.Sequence, ev.Sequence+1)
	}
	reqs := outboxByMethod(t, acc, "REQUEST")
	if len(reqs) != 2 {
		t.Fatalf("REQUEST rows=%d, want 2; %#v", len(reqs), reqs)
	}
	keys := map[string]bool{}
	for _, row := range reqs {
		keys[row.IdempotencyKey] = true
	}
	want0 := "REQUEST|" + ev.UID + "|0|mailto:att@example.com"
	want1 := "REQUEST|" + ev.UID + "|1|mailto:att@example.com"
	if !keys[want0] || !keys[want1] {
		t.Fatalf("keys %v, want %q and %q", keys, want0, want1)
	}
}

func TestEnqueueOutboxPutDeleteObject(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	ics := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:outbox-obj@mox\r\nSEQUENCE:0\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:CalDAV\r\nORGANIZER:mailto:org@mox.example\r\nATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:att@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	obj, err := svc.PutObject(testctx, acc, "outbox-obj@mox", ics, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutObject(testctx, acc, "outbox-obj@mox", ics, obj.ETag); err != nil {
		t.Fatal(err)
	}
	reqs := outboxByMethod(t, acc, "REQUEST")
	if len(reqs) != 1 || reqs[0].IdempotencyKey != "REQUEST|outbox-obj@mox|0|mailto:att@example.com" {
		t.Fatalf("PutObject REQUEST: %#v", reqs)
	}
	if err := svc.DeleteObject(testctx, acc, "outbox-obj@mox", ""); err != nil {
		t.Fatal(err)
	}
	cancels := outboxByMethod(t, acc, "CANCEL")
	if len(cancels) != 1 || cancels[0].IdempotencyKey != "CANCEL|outbox-obj@mox|0|mailto:att@example.com" {
		t.Fatalf("DeleteObject CANCEL: %#v", cancels)
	}
}

func TestEnqueueOutboxSkipsOrganizerAttendee(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	in := inviteInput("outbox-org@mox", "mailto:Org@Mox.example", "mailto:Att@Example.COM")
	in.Attendees = append(in.Attendees, store.CalendarAttendee{Address: "mailto:org@mox.example"})
	ev, err := (Service{}).Create(testctx, acc, in)
	if err != nil {
		t.Fatal(err)
	}
	reqs := outboxByMethod(t, acc, "REQUEST")
	if len(reqs) != 1 {
		t.Fatalf("REQUEST rows=%d, want 1 (skip organizer); %#v", len(reqs), reqs)
	}
	if reqs[0].Recipient != "mailto:att@example.com" {
		t.Fatalf("recipient %q", reqs[0].Recipient)
	}
	if reqs[0].IdempotencyKey != "REQUEST|"+ev.UID+"|0|mailto:att@example.com" {
		t.Fatalf("key %q", reqs[0].IdempotencyKey)
	}
}

func TestFlushOutboxQueueNotInitialized(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	if _, err := (Service{}).Create(testctx, acc, inviteInput("outbox-flush@mox", "mailto:org@mox.example", "mailto:att@example.com")); err != nil {
		t.Fatal(err)
	}
	if err := FlushOutbox(testctx, mlog.New("calendar", nil), acc); err == nil {
		t.Fatal("FlushOutbox: want error when queue is not initialized")
	}
	rows := outboxByMethod(t, acc, "REQUEST")
	if len(rows) != 1 || rows[0].Queued {
		t.Fatalf("queue not init must leave Queued=false: %#v", rows)
	}
}

func TestEnqueueOutboxRespondUpdatesUnqueuedReply(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	ev, err := svc.Create(testctx, acc, inviteInput("outbox-re-reply@mox", "mailto:org@mox.example", "mailto:att@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	ev, err = svc.Respond(testctx, acc, ev.ID, ev.Revision, "mailto:att@example.com", "ACCEPTED")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Respond(testctx, acc, ev.ID, ev.Revision, "mailto:att@example.com", "DECLINED"); err != nil {
		t.Fatal(err)
	}
	replies := outboxByMethod(t, acc, "REPLY")
	if len(replies) != 1 {
		t.Fatalf("REPLY rows=%d, want 1; %#v", len(replies), replies)
	}
	reply := replies[0]
	if reply.Queued {
		t.Fatalf("Queued: %#v", reply)
	}
	if reply.IdempotencyKey != "REPLY|"+ev.UID+"|0|mailto:org@mox.example" {
		t.Fatalf("IdempotencyKey %q", reply.IdempotencyKey)
	}
	cal, err := icalendar.Parse(reply.ICS)
	if err != nil || cal.Method() != "REPLY" {
		t.Fatalf("REPLY ICS method=%q err=%v\n%s", cal.Method(), err, reply.ICS)
	}
	if _, ok := cal.Prop("PRODID"); !ok {
		t.Fatalf("REPLY ICS missing PRODID:\n%s", reply.ICS)
	}
	if !bytes.Contains(reply.ICS, []byte("PARTSTAT=DECLINED")) {
		t.Fatalf("REPLY ICS PARTSTAT not DECLINED:\n%s", reply.ICS)
	}
	if bytes.Contains(reply.ICS, []byte("PARTSTAT=ACCEPTED")) {
		t.Fatalf("REPLY ICS still ACCEPTED:\n%s", reply.ICS)
	}
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

func TestApplyIncomingEmptyOrganizerEmptyMailFrom(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	meta := IncomingMeta{MailFrom: "", MsgFrom: "spoof@example.com"}

	req := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:imip-empty-org@mox\r\nSEQUENCE:1\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Unauth\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err := ApplyIncoming(testctx, acc, req, meta)
	if err != nil || result != "ignored-mismatch" {
		t.Fatalf("empty ORGANIZER REQUEST: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REQUEST", "imip-empty-org@mox", 1, "ignored-mismatch")
	if _, err := svc.GetObject(testctx, acc, "imip-empty-org@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty ORGANIZER REQUEST wrote object: %v", err)
	}

	seed := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:imip-empty-cancel@mox\r\nSEQUENCE:1\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Keep\r\nORGANIZER:mailto:org@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, err := svc.PutObject(testctx, acc, "imip-empty-cancel@mox", seed, ""); err != nil {
		t.Fatal(err)
	}
	cancel := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:imip-empty-cancel@mox\r\nSEQUENCE:1\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err = ApplyIncoming(testctx, acc, cancel, meta)
	if err != nil || result != "ignored-mismatch" {
		t.Fatalf("empty ORGANIZER CANCEL: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "CANCEL", "imip-empty-cancel@mox", 1, "ignored-mismatch")
	if _, err := svc.GetObject(testctx, acc, "imip-empty-cancel@mox"); err != nil {
		t.Fatalf("empty ORGANIZER CANCEL deleted object: %v", err)
	}
}

func TestApplyIncomingThisAndFutureCancel(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	start := icalendar.DateTime{UTC: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)}
	end := icalendar.DateTime{UTC: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)}
	master, err := svc.Create(testctx, acc, EventInput{
		UID:       "imip-taf-cancel@mox",
		Summary:   "Series",
		Organizer: "mailto:org@mox.example",
		Start:     start,
		End:       end,
		RRULE:     "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(testctx, acc, EventInput{
		UID:          master.UID,
		RecurrenceID: "20260816T090000Z",
		Summary:      "Earlier exception",
		Organizer:    "mailto:org@mox.example",
		Start:        icalendar.DateTime{UTC: time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)},
		End:          icalendar.DateTime{UTC: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Create(testctx, acc, EventInput{
		UID:          master.UID,
		RecurrenceID: "20260818T090000Z",
		Summary:      "Later exception",
		Organizer:    "mailto:org@mox.example",
		Start:        icalendar.DateTime{UTC: time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)},
		End:          icalendar.DateTime{UTC: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeOutbox := len(outboxRows(t, acc))

	cancel := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:imip-taf-cancel@mox\r\nSEQUENCE:0\r\nDTSTART:20260817T090000Z\r\nDTEND:20260817T100000Z\r\nRECURRENCE-ID;RANGE=THISANDFUTURE:20260817T090000Z\r\nORGANIZER:mailto:org@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err := ApplyIncoming(testctx, acc, cancel, IncomingMeta{MailFrom: "org@mox.example"})
	if err != nil || result != "applied" {
		t.Fatalf("THISANDFUTURE CANCEL: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "CANCEL", "imip-taf-cancel@mox", 0, "applied")
	if n := len(outboxRows(t, acc)); n != beforeOutbox {
		t.Fatalf("THISANDFUTURE CANCEL enqueued outbox: before=%d after=%d", beforeOutbox, n)
	}

	var got store.CalendarEvent
	var keptException bool
	err = acc.DB.Read(testctx, func(tx *bstore.Tx) error {
		events, err := eventsByUID(tx, master.UID)
		if err != nil {
			return err
		}
		for _, ev := range events {
			switch ev.RecurrenceID {
			case "":
				got = ev
			case "20260816T090000Z":
				keptException = true
			default:
				t.Errorf("kept exception Recurrence-ID %q", ev.RecurrenceID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !keptException {
		t.Fatal("dropped exception before Recurrence-ID")
	}
	if got.Sequence != master.Sequence+1 {
		t.Fatalf("THISANDFUTURE CANCEL sequence=%d, want %d", got.Sequence, master.Sequence+1)
	}
	if !strings.Contains(got.RRULE, "UNTIL=20260817T085959Z") {
		t.Fatalf("THISANDFUTURE CANCEL RRULE %q, want UNTIL=20260817T085959Z", got.RRULE)
	}
	if strings.Contains(got.RRULE, "COUNT=") {
		t.Fatalf("THISANDFUTURE CANCEL left COUNT in RRULE %q", got.RRULE)
	}

	day := func(d int) (time.Time, time.Time) {
		from := time.Date(2026, 8, d, 0, 0, 0, 0, time.UTC)
		return from, from.AddDate(0, 0, 1)
	}
	queryDay := func(d int) []Instance {
		t.Helper()
		from, to := day(d)
		inst, err := svc.Query(testctx, acc, from, to)
		if err != nil {
			t.Fatal(err)
		}
		return inst
	}

	early := queryDay(16)
	if len(early) != 1 || !early[0].Start.UTC.Equal(time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)) || !early[0].Exception {
		t.Fatalf("earlier instance: %#v", early)
	}
	if inst := queryDay(17); len(inst) != 0 {
		t.Fatalf("Recurrence-ID instant still queryable: %#v", inst)
	}
	if inst := queryDay(18); len(inst) != 0 {
		t.Fatalf("later instance still queryable: %#v", inst)
	}
	if inst := queryDay(15); len(inst) != 1 || !inst[0].Start.UTC.Equal(start.UTC) {
		t.Fatalf("first instance: %#v", inst)
	}
}

func TestApplyIncomingStaleCancel(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	svc := Service{}
	seed := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:imip-stale-cancel@mox\r\nSEQUENCE:2\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Current\r\nORGANIZER:mailto:org@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	if _, err := svc.PutObject(testctx, acc, "imip-stale-cancel@mox", seed, ""); err != nil {
		t.Fatal(err)
	}
	cancel := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:CANCEL\r\nBEGIN:VEVENT\r\nUID:imip-stale-cancel@mox\r\nSEQUENCE:1\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nORGANIZER:mailto:org@mox.example\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err := ApplyIncoming(testctx, acc, cancel, IncomingMeta{MailFrom: "org@mox.example"})
	if err != nil || result != "ignored-stale" {
		t.Fatalf("stale CANCEL: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "CANCEL", "imip-stale-cancel@mox", 1, "ignored-stale")
	got := eventByUID(t, acc, "imip-stale-cancel@mox")
	if got.Sequence != 2 {
		t.Fatalf("stale CANCEL deleted or replaced event: %#v", got)
	}
}

func TestApplyIncomingPutObjectInvalidRecordsInbound(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	cal := parseCal(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:imip-invalid@mox\r\nSEQUENCE:1\r\nORGANIZER:mailto:org@mox.example\r\nSUMMARY:Bad\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	result, err := ApplyIncoming(testctx, acc, cal, IncomingMeta{MailFrom: "org@mox.example"})
	if err != nil || result != "ignored-mismatch" {
		t.Fatalf("invalid PutObject: result=%q err=%v", result, err)
	}
	inboundHas(t, acc, "REQUEST", "imip-invalid@mox", 1, "ignored-mismatch")
	if _, err := (Service{}).GetObject(testctx, acc, "imip-invalid@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid PutObject wrote object: %v", err)
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
