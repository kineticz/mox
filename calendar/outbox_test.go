package calendar

import (
	"bytes"
	"context"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/mtastsdb"
	"github.com/mjl-/mox/queue"
	"github.com/mjl-/mox/store"
	"github.com/mjl-/mox/tlsrptdb"
)

func TestBuildIMIPMessage(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//caldav//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:u@mox\r\nSUMMARY:Meet\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	msg, err := buildIMIPMessage("mjl@mox.example", "att@example.com", "Invitation: Meet", "REQUEST", ics)
	if err != nil {
		t.Fatalf("buildIMIPMessage: %v", err)
	}
	if !bytes.Contains(msg, []byte("method=REQUEST")) {
		t.Fatalf("Content-Type missing method=REQUEST:\n%s", msg)
	}
	if !bytes.Contains(msg, []byte("BEGIN:VCALENDAR")) {
		t.Fatalf("body missing BEGIN:VCALENDAR:\n%s", msg)
	}
}

func TestBuildIMIPMessageSubjectInjection(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	msg, err := buildIMIPMessage("mjl@mox.example", "att@example.com", "Invitation: Meet\nBcc: evil@x.com", "REQUEST", ics)
	if err != nil {
		t.Fatalf("buildIMIPMessage: %v", err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, msg)
	}
	if got := parsed.Header.Get("Bcc"); got != "" {
		t.Fatalf("SUMMARY injected Bcc %q:\n%s", got, msg)
	}
	if strings.Contains(string(msg), "\nBcc:") {
		t.Fatalf("raw Bcc header line:\n%s", msg)
	}
}

func TestBuildIMIPMessageMethod(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	for _, method := range []string{"REQUEST", "REPLY", "CANCEL"} {
		msg, err := buildIMIPMessage("mjl@mox.example", "att@example.com", "x", method, ics)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		want := "method=" + method
		if !bytes.Contains(msg, []byte(want)) {
			t.Fatalf("%s: missing %s:\n%s", method, want, msg)
		}
	}
}

func setupQueue(t *testing.T) (*store.Account, func()) {
	t.Helper()
	os.RemoveAll("../testdata/queue/data")
	log := mlog.New("calendar", nil)
	mox.Context = testctx
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/queue/mox.conf")
	mox.MustLoadConfig(true, false)
	mox.Conf.Calendar.EnableCalendar = true
	mox.ApplyCalendarDefaults(&mox.Conf.Calendar)
	mox.Shutdown, mox.ShutdownCancel = context.WithCancel(testctx)
	err := queue.Init()
	if err != nil {
		t.Fatalf("queue init: %v", err)
	}
	err = mtastsdb.Init(false)
	if err != nil {
		t.Fatalf("mtastsdb init: %v", err)
	}
	err = tlsrptdb.Init()
	if err != nil {
		t.Fatalf("tlsrptdb init: %v", err)
	}
	switchStop := store.Switchboard()

	acc, err := store.OpenAccount(log, "mjl", false)
	if err != nil {
		t.Fatalf("open account: %v", err)
	}
	err = acc.SetPassword(log, "testtest")
	if err != nil {
		t.Fatalf("set password: %v", err)
	}

	return acc, func() {
		acc.Close()
		acc.WaitClosed()
		mox.ShutdownCancel()
		mox.Shutdown, mox.ShutdownCancel = context.WithCancel(testctx)
		queue.Shutdown()
		if err := mtastsdb.Close(); err != nil {
			t.Errorf("mtastsdb close: %v", err)
		}
		if err := tlsrptdb.Close(); err != nil {
			t.Errorf("tlsrptdb close: %v", err)
		}
		switchStop()
	}
}

func TestFlushOutboxAdd(t *testing.T) {
	acc, cleanup := setupQueue(t)
	defer cleanup()
	enableIMIP(t)

	if _, err := (Service{}).Create(testctx, acc, inviteInput("outbox-queue@mox", "mailto:mjl@mox.example", "mailto:att@example.com")); err != nil {
		t.Fatal(err)
	}
	rows := outboxByMethod(t, acc, "REQUEST")
	if len(rows) != 1 || !rows[0].Queued {
		t.Fatalf("after Create+flush: %#v", rows)
	}

	msgs, err := queue.List(testctx, queue.Filter{}, queue.Sort{})
	if err != nil {
		t.Fatalf("queue list: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("queue messages=%d, want 1", len(msgs))
	}
	if msgs[0].SenderAccount != "mjl" {
		t.Fatalf("SenderAccount %q", msgs[0].SenderAccount)
	}
	if msgs[0].Sender().XString(true) != "mjl@mox.example" {
		t.Fatalf("From %q, want account organizer", msgs[0].Sender().XString(true))
	}
	if msgs[0].Subject != "Invitation: Meet" {
		t.Fatalf("Subject %q", msgs[0].Subject)
	}
	if msgs[0].Recipient().XString(true) != "att@example.com" {
		t.Fatalf("recipient %q", msgs[0].Recipient().XString(true))
	}
	queued, err := os.ReadFile(msgs[0].MessagePath())
	if err != nil {
		t.Fatalf("read queued message: %v", err)
	}
	if !bytes.Contains(queued, []byte("method=REQUEST")) || !bytes.Contains(queued, []byte("BEGIN:VCALENDAR")) {
		t.Fatalf("queued MIME:\n%s", queued)
	}

	if err := FlushOutbox(testctx, mlog.New("calendar", nil), acc); err != nil {
		t.Fatalf("second FlushOutbox: %v", err)
	}
	msgs, err = queue.List(testctx, queue.Filter{}, queue.Sort{})
	if err != nil {
		t.Fatalf("queue list after second flush: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("second flush queued extra messages: %d", len(msgs))
	}
}

func TestFlushOutboxReplyFrom(t *testing.T) {
	acc, cleanup := setupQueue(t)
	defer cleanup()
	enableIMIP(t)
	svc := Service{}
	ev, err := svc.Create(testctx, acc, inviteInput("outbox-reply-from@mox", "mailto:org@example.com", "mailto:mjl@mox.example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Respond(testctx, acc, ev.ID, ev.Revision, "mailto:mjl@mox.example", "ACCEPTED"); err != nil {
		t.Fatal(err)
	}
	replies := outboxByMethod(t, acc, "REPLY")
	if len(replies) != 1 || !replies[0].Queued {
		t.Fatalf("REPLY row: %#v", replies)
	}

	msgs, err := queue.List(testctx, queue.Filter{}, queue.Sort{})
	if err != nil {
		t.Fatalf("queue list: %v", err)
	}
	var reply *queue.Msg
	for i := range msgs {
		if msgs[i].Subject == "Accepted: Meet" {
			m := msgs[i]
			reply = &m
			break
		}
	}
	if reply == nil {
		t.Fatalf("no REPLY in queue: %#v", msgs)
	}
	if got := reply.Sender().XString(true); got != "mjl@mox.example" {
		t.Fatalf("REPLY From %q, want account attendee (not organizer)", got)
	}
	if got := reply.Recipient().XString(true); got != "org@example.com" {
		t.Fatalf("REPLY To %q", got)
	}
}

func TestFlushOutboxSubjectInjection(t *testing.T) {
	acc, cleanup := setupQueue(t)
	defer cleanup()
	enableIMIP(t)
	in := inviteInput("outbox-subj@mox", "mailto:mjl@mox.example", "mailto:att@example.com")
	in.Summary = "Meet\nBcc: evil@x.com"
	if _, err := (Service{}).Create(testctx, acc, in); err != nil {
		t.Fatal(err)
	}
	msgs, err := queue.List(testctx, queue.Filter{}, queue.Sort{})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("queue list: n=%d err=%v", len(msgs), err)
	}
	queued, err := os.ReadFile(msgs[0].MessagePath())
	if err != nil {
		t.Fatalf("read queued: %v", err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(queued))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, queued)
	}
	if got := parsed.Header.Get("Bcc"); got != "" {
		t.Fatalf("SUMMARY injected Bcc %q:\n%s", got, queued)
	}
}

func TestClaimOutboxRowOnce(t *testing.T) {
	acc := testAccount(t)
	row := store.CalendarOutbox{
		Method:         "REQUEST",
		UID:            "claim@mox",
		Recipient:      "mailto:att@example.com",
		ICS:            []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"),
		IdempotencyKey: "REQUEST|claim@mox|0|mailto:att@example.com",
	}
	err := acc.DB.Write(testctx, func(tx *bstore.Tx) error {
		return tx.Insert(&row)
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var first, second bool
	err = acc.DB.Write(testctx, func(tx *bstore.Tx) error {
		var err error
		first, err = claimOutboxRow(tx, row.ID)
		return err
	})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	err = acc.DB.Write(testctx, func(tx *bstore.Tx) error {
		var err error
		second, err = claimOutboxRow(tx, row.ID)
		return err
	})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if !first || second {
		t.Fatalf("claims first=%v second=%v, want true then false", first, second)
	}
}

func TestFlushOutboxDisabled(t *testing.T) {
	acc := testAccount(t)
	if err := FlushOutbox(testctx, mlog.New("calendar", nil), acc); err != nil {
		t.Fatalf("IMIP off: %v", err)
	}
}
