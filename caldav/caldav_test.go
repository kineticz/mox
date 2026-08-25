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
	if rec.Body.String() != "" {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func propfind(t *testing.T, path, depth, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PROPFIND", path, strings.NewReader(body))
	req.SetBasicAuth("mjl@mox.example", "test1234")
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml")
	Handler(false).ServeHTTP(rec, req)
	return rec
}

func TestPROPFINDCurrentUserPrincipal(t *testing.T) {
	setupCalDAV(t, true, true)
	body := `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:current-user-principal/>
  </D:prop>
</D:propfind>`
	rec := propfind(t, "/", "0", body)
	if rec.Code != 207 {
		t.Fatalf("got %d body %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "/caldav/mjl/") {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestPROPFINDCalendarCollection(t *testing.T) {
	setupCalDAV(t, true, true)
	body := `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
  <D:prop>
    <CS:getctag/>
    <D:resourcetype/>
  </D:prop>
</D:propfind>`
	rec := propfind(t, "/mjl/calendars/default/", "0", body)
	if rec.Code != 207 {
		t.Fatalf("got %d body %q", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, "resourcetype") {
		t.Fatalf("missing resourcetype: %q", got)
	}
	// CalDAV calendar element in resourcetype, not the "calendars" path segment.
	if !strings.Contains(got, ":calendar") && !strings.Contains(got, "<calendar") {
		t.Fatalf("missing calendar resourcetype: %q", got)
	}
	if !strings.Contains(got, "getctag") {
		t.Fatalf("missing getctag: %q", got)
	}
}

func TestPROPFINDOtherAccountForbidden(t *testing.T) {
	setupCalDAV(t, true, true)
	body := `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:resourcetype/>
  </D:prop>
</D:propfind>`
	rec := propfind(t, "/otheruser/", "0", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d body %q", rec.Code, rec.Body.String())
	}
}

func TestPROPFINDCalendarHomeDepth1(t *testing.T) {
	setupCalDAV(t, true, true)
	rec := propfind(t, "/mjl/calendars/", "1", "")
	if rec.Code != 207 {
		t.Fatalf("got %d body %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/caldav/mjl/calendars/default/") {
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
