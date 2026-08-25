package calendar

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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

func TestFlushOutboxDisabled(t *testing.T) {
	acc := testAccount(t)
	if err := FlushOutbox(testctx, mlog.New("calendar", nil), acc); err != nil {
		t.Fatalf("IMIP off: %v", err)
	}
}
