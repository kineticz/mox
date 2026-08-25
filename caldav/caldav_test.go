package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

var ctxbg = context.Background()

func tcheck(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %s", msg, err)
	}
}

func setupCalDAV(t *testing.T, enableCalendar, enableCalDAV bool) {
	t.Helper()

	mox.LimitersInit()
	os.RemoveAll("../testdata/caldav/data")
	mox.Context = ctxbg
	mox.ConfigStaticPath = filepath.FromSlash("../testdata/caldav/mox.conf")
	mox.ConfigDynamicPath = filepath.FromSlash("../testdata/caldav/domains.conf")
	mox.MustLoadConfig(true, false)
	mox.Conf.Calendar.EnableCalendar = enableCalendar
	mox.Conf.Calendar.EnableCalDAV = enableCalDAV
	mox.ApplyCalendarDefaults(&mox.Conf.Calendar)

	err := store.Init(ctxbg)
	tcheck(t, err, "store init")
	t.Cleanup(func() {
		err := store.Close()
		tcheck(t, err, "store close")
	})
	t.Cleanup(store.Switchboard())

	log := mlog.New("caldav", nil)
	acc, err := store.OpenAccount(log, "mjl", false)
	tcheck(t, err, "open account")
	err = acc.SetPassword(log, "test1234")
	tcheck(t, err, "set password")
	t.Cleanup(func() {
		err := acc.Close()
		log.Check(err, "closing account")
		acc.WaitClosed()
	})
}

func TestHandlerDisabledNotFound(t *testing.T) {
	setupCalDAV(t, false, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/caldav/", nil)
	Handler(false).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestOptionsUnauthenticated(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	Handler(false).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("DAV"); !strings.Contains(got, "calendar-access") {
		t.Fatalf("DAV %q", got)
	}
}

func TestGetRequiresAuth(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Handler(false).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestOptionsAllowAndEmptyBody(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/mjl/calendars/default/", nil)
	Handler(false).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("DAV"); got != "1, 3, calendar-access" {
		t.Fatalf("DAV %q", got)
	}
	if got := rec.Header().Get("Allow"); got != "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT" {
		t.Fatalf("Allow %q", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestGetInvalidAuth(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("mjl@mox.example", "wrong")
	Handler(false).ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("got %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate")
	}
}

func TestGetAuthenticated(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("mjl@mox.example", "test1234")
	Handler(false).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type %q", ct)
	}
	if rec.Body.String() != "caldav" {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestWellKnownRedirect(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/caldav", nil)
	WellKnown(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/caldav/" {
		t.Fatalf("Location %q", got)
	}
}

func TestWellKnownDisabled(t *testing.T) {
	setupCalDAV(t, false, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/caldav", nil)
	WellKnown(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestWellKnownMethodNotAllowed(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/.well-known/caldav", nil)
	WellKnown(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", rec.Code)
	}
}
