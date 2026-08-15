package webmail

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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
