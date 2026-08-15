package webmail

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mjl-/mox/calendar"
	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/mtastsdb"
	"github.com/mjl-/mox/store"
)

func testCalendarAPISetup(t *testing.T) (context.Context, *store.Account) {
	t.Helper()

	mox.LimitersInit()
	os.RemoveAll("../testdata/webmail/data")
	mox.Context = ctxbg
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/webmail/mox.conf")
	mox.ConfigDynamicPath = filepath.FromSlash("../testdata/webmail/domains.conf")
	mox.MustLoadConfig(true, false)
	mox.Conf.Calendar.EnableCalendar = true
	mox.ApplyCalendarDefaults(&mox.Conf.Calendar)

	err := store.Init(ctxbg)
	tcheck(t, err, "store init")
	t.Cleanup(func() {
		err := store.Close()
		tcheck(t, err, "store close")
	})
	t.Cleanup(store.Switchboard())

	log := mlog.New("webmail", nil)
	err = mtastsdb.Init(false)
	tcheck(t, err, "mtastsdb init")
	t.Cleanup(func() {
		err := mtastsdb.Close()
		tcheck(t, err, "mtastsdb close")
	})

	acc, err := store.OpenAccount(log, "mjl", false)
	tcheck(t, err, "open account")
	t.Cleanup(func() {
		err := acc.Close()
		pkglog.Check(err, "closing account")
		acc.WaitClosed()
	})

	reqInfo := requestInfo{log, "mjl@mox.example", acc, "", nil, &http.Request{RemoteAddr: "127.0.0.1:1234"}}
	ctx := context.WithValue(ctxbg, requestInfoCtxKey, reqInfo)
	return ctx, acc
}

func TestCalendarAPI(t *testing.T) {
	ctx, _ := testCalendarAPISetup(t)

	w := Webmail{maxMessageSize: 1024 * 1024}
	if !w.CalendarEnabled(ctx) {
		t.Fatal("expected enabled")
	}
	created := w.CalendarCreate(ctx, CalendarEventView{
		Summary: "API",
		Start:   CalendarTime{Instant: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)},
		End:     CalendarTime{Instant: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)},
	})
	if created.ID == 0 || created.Revision != 1 {
		t.Fatalf("%#v", created)
	}
	list := w.CalendarQuery(ctx, time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC))
	if len(list) != 1 || list[0].Summary != "API" {
		t.Fatalf("%#v", list)
	}
	tneedError(t, func() { w.CalendarDelete(ctx, created.ID, created.Revision+99, true) })
	w.CalendarDelete(ctx, created.ID, created.Revision, true)
}

func TestCalendarAPIDisabled(t *testing.T) {
	ctx, _ := testCalendarAPISetup(t)

	mox.Conf.Calendar.EnableCalendar = false
	w := Webmail{}
	tneedError(t, func() { w.CalendarQuery(ctx, time.Now(), time.Now().Add(time.Hour)) })
}

func TestCalendarAPIRespond(t *testing.T) {
	ctx, acc := testCalendarAPISetup(t)
	w := Webmail{maxMessageSize: 1024 * 1024}

	start := icalendar.DateTime{UTC: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)}
	end := icalendar.DateTime{UTC: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)}

	svc := calendar.Service{}
	ev, err := svc.Create(ctx, acc, calendar.EventInput{
		Summary:   "Meet",
		Class:     "PRIVATE",
		Organizer: "other@mox.example",
		Start:     start,
		End:       end,
		Attendees: []store.CalendarAttendee{
			{Address: "mjl@mox.example", PartStat: "NEEDS-ACTION"},
			{Address: "guest@mox.example", PartStat: "NEEDS-ACTION"},
		},
	})
	tcheck(t, err, "create event")
	if ev.Sequence != 0 {
		t.Fatalf("sequence: %d", ev.Sequence)
	}

	responded := w.CalendarRespond(ctx, ev.ID, ev.Revision, "ACCEPTED")
	if responded.Sequence != ev.Sequence {
		t.Fatalf("sequence changed: %d -> %d", ev.Sequence, responded.Sequence)
	}
	if responded.Revision != ev.Revision+1 {
		t.Fatalf("revision: got %d want %d", responded.Revision, ev.Revision+1)
	}
	if len(responded.Attendees) != 2 {
		t.Fatalf("attendees: %#v", responded.Attendees)
	}
	if responded.Attendees[0].Address != "mjl@mox.example" || responded.Attendees[0].PartStat != "ACCEPTED" {
		t.Fatalf("self attendee: %#v", responded.Attendees[0])
	}
	if responded.Attendees[1].PartStat != "NEEDS-ACTION" {
		t.Fatalf("other attendee changed: %#v", responded.Attendees[1])
	}

	got, err := svc.Get(ctx, acc, ev.ID)
	tcheck(t, err, "get event")
	if got.Class != "PRIVATE" {
		t.Fatalf("class cleared: %q", got.Class)
	}
	if !strings.Contains(string(got.ICS), "CLASS:PRIVATE\r\n") {
		t.Fatalf("ICS missing CLASS:\n%s", got.ICS)
	}
	if !strings.Contains(string(got.ICS), "PARTSTAT=ACCEPTED") {
		t.Fatalf("ICS missing accepted partstat:\n%s", got.ICS)
	}

	orgEv := w.CalendarCreate(ctx, CalendarEventView{
		Summary:   "Organizer",
		Organizer: "mjl@mox.example",
		Start:     CalendarTime{Instant: start.UTC},
		End:       CalendarTime{Instant: end.UTC},
		Attendees: []CalendarAttendeeView{{Address: "guest@mox.example", PartStat: "NEEDS-ACTION"}},
	})
	tneedError(t, func() { w.CalendarRespond(ctx, orgEv.ID, orgEv.Revision, "ACCEPTED") })

	noAttEv := w.CalendarCreate(ctx, CalendarEventView{
		Summary: "No self",
		Start:   CalendarTime{Instant: start.UTC},
		End:     CalendarTime{Instant: end.UTC},
		Attendees: []CalendarAttendeeView{
			{Address: "guest@mox.example", PartStat: "NEEDS-ACTION"},
		},
	})
	tneedError(t, func() { w.CalendarRespond(ctx, noAttEv.ID, noAttEv.Revision, "ACCEPTED") })

	tneedError(t, func() { w.CalendarRespond(ctx, ev.ID, responded.Revision+99, "DECLINED") })
	tneedError(t, func() { w.CalendarRespond(ctx, ev.ID, responded.Revision, "MAYBE") })
}
