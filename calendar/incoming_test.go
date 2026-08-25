package calendar

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/smtp"
	"github.com/mjl-/mox/store"
)

const requestICS = `BEGIN:VCALENDAR
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:imip-mail@mox
SEQUENCE:1
DTSTART:20260815T090000Z
DTEND:20260815T100000Z
SUMMARY:Invite
ORGANIZER:mailto:org@mox.example
ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:mjl@mox.example
END:VEVENT
END:VCALENDAR
`

func incomingMIME(ics string) string {
	return strings.ReplaceAll(`From: org@mox.example
To: mjl@mox.example
Subject: Invitation: Invite
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="bnd"

--bnd
Content-Type: text/plain; charset=utf-8

Please see the invitation.
--bnd
Content-Type: text/calendar; charset=utf-8; method=REQUEST

`+ics+`--bnd--
`, "\n", "\r\n")
}

func writeIncomingFile(t *testing.T, raw string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "imip-in")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	return f
}

func incomingStoreMsg(mailFrom, msgFromLocal, msgFromDomain string, validated bool) *store.Message {
	return &store.Message{
		MailFrom:         mailFrom,
		MsgFromLocalpart: smtp.Localpart(msgFromLocal),
		MsgFromDomain:    msgFromDomain,
		MsgFromValidated: validated,
	}
}

func TestProcessIncomingMailAppliesREQUEST(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	m := incomingStoreMsg("org@mox.example", "org", "mox.example", true)
	f := writeIncomingFile(t, incomingMIME(requestICS))

	err := ProcessIncomingMail(testctx, mlog.New("calendar", nil), acc, m, f)
	if err != nil {
		t.Fatalf("ProcessIncomingMail: %v", err)
	}
	if _, err := (Service{}).GetObject(testctx, acc, "imip-mail@mox"); err != nil {
		t.Fatalf("applied object: %v", err)
	}
	inboundHas(t, acc, "REQUEST", "imip-mail@mox", 1, "applied")
	if rows := outboxRows(t, acc); len(rows) != 0 {
		t.Fatalf("incoming REQUEST enqueued %#v", rows)
	}
}

func TestProcessIncomingMailDisabled(t *testing.T) {
	acc := testAccount(t)
	m := incomingStoreMsg("org@mox.example", "org", "mox.example", true)
	f := writeIncomingFile(t, incomingMIME(requestICS))

	err := ProcessIncomingMail(testctx, mlog.New("calendar", nil), acc, m, f)
	if err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if inboundCount(t, acc) != 0 {
		t.Fatal("disabled wrote inbound")
	}
	if _, err := (Service{}).GetObject(testctx, acc, "imip-mail@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled wrote object: %v", err)
	}
}

func TestProcessIncomingMailMismatchReturnsNil(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	m := incomingStoreMsg("evil@example.com", "evil", "example.com", false)
	f := writeIncomingFile(t, incomingMIME(requestICS))

	err := ProcessIncomingMail(testctx, mlog.New("calendar", nil), acc, m, f)
	if err != nil {
		t.Fatalf("mismatch must not fail SMTP: %v", err)
	}
	inboundHas(t, acc, "REQUEST", "imip-mail@mox", 1, "ignored-mismatch")
	if _, err := (Service{}).GetObject(testctx, acc, "imip-mail@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mismatch wrote object: %v", err)
	}
}

// Mail the junk filter rejected must not mutate the calendar; the mail itself
// is still kept.
func TestProcessIncomingMailSkipsJunk(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	m := incomingStoreMsg("org@mox.example", "org", "mox.example", true)
	m.Junk = true
	f := writeIncomingFile(t, incomingMIME(requestICS))

	err := ProcessIncomingMail(testctx, mlog.New("calendar", nil), acc, m, f)
	if err != nil {
		t.Fatalf("junk must not fail SMTP: %v", err)
	}
	if inboundCount(t, acc) != 0 {
		t.Fatal("junk message was applied")
	}
	if _, err := (Service{}).GetObject(testctx, acc, "imip-mail@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("junk message wrote object: %v", err)
	}
}

func TestProcessIncomingMailSkipsOversize(t *testing.T) {
	acc := testAccount(t)
	enableIMIP(t)
	orig := mox.Conf.Calendar.MaxICSBytes
	mox.Conf.Calendar.MaxICSBytes = 50
	t.Cleanup(func() { mox.Conf.Calendar.MaxICSBytes = orig })

	m := incomingStoreMsg("org@mox.example", "org", "mox.example", true)
	f := writeIncomingFile(t, incomingMIME(requestICS))

	err := ProcessIncomingMail(testctx, mlog.New("calendar", nil), acc, m, f)
	if err != nil {
		t.Fatalf("oversize: %v", err)
	}
	if inboundCount(t, acc) != 0 {
		t.Fatal("oversize part was applied")
	}
	if _, err := (Service{}).GetObject(testctx, acc, "imip-mail@mox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oversize wrote object: %v", err)
	}
}
