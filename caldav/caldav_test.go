package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func objectReq(t *testing.T, method, path, body string, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.SetBasicAuth("mjl@mox.example", "test1234")
	for k, vv := range hdr {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	Handler(false).ServeHTTP(rec, req)
	return rec
}

func TestPutGetDeleteObject(t *testing.T) {
	setupCalDAV(t, true, true)

	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:obj1@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Hello\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	path := "/mjl/calendars/default/" + url.PathEscape("obj1@mox") + ".ics"

	putHdr := http.Header{}
	putHdr.Set("Content-Type", "text/calendar")
	rec := objectReq(t, http.MethodPut, path, ics, putHdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("put got %d body %q", rec.Code, rec.Body.String())
	}

	rec = objectReq(t, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get got %d body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/calendar; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if !strings.Contains(rec.Body.String(), "BEGIN:VEVENT") || !strings.Contains(rec.Body.String(), "UID:obj1@mox") {
		t.Fatalf("body %q", rec.Body.String())
	}

	head := objectReq(t, http.MethodHead, path, "", nil)
	if head.Code != http.StatusOK {
		t.Fatalf("head got %d", head.Code)
	}
	if head.Header().Get("ETag") != etag {
		t.Fatalf("head ETag %q", head.Header().Get("ETag"))
	}
	if head.Body.Len() != 0 {
		t.Fatalf("head body %q", head.Body.String())
	}

	staleHdr := http.Header{}
	staleHdr.Set("Content-Type", "text/calendar")
	staleHdr.Set("If-Match", `"rev-999"`)
	rec = objectReq(t, http.MethodPut, path, ics, staleHdr)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale put got %d body %q", rec.Code, rec.Body.String())
	}

	delHdr := http.Header{}
	delHdr.Set("If-Match", etag)
	rec = objectReq(t, http.MethodDelete, path, "", delHdr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete got %d body %q", rec.Code, rec.Body.String())
	}

	rec = objectReq(t, http.MethodGet, path, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete got %d body %q", rec.Code, rec.Body.String())
	}
}

func TestPutGetDeleteObjectUIDMismatch(t *testing.T) {
	setupCalDAV(t, true, true)

	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:other@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Hello\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	path := "/mjl/calendars/default/" + url.PathEscape("obj1@mox") + ".ics"
	hdr := http.Header{}
	hdr.Set("Content-Type", "text/calendar")
	rec := objectReq(t, http.MethodPut, path, ics, hdr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("uid mismatch put got %d body %q", rec.Code, rec.Body.String())
	}
}

func report(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("REPORT", path, strings.NewReader(body))
	req.SetBasicAuth("mjl@mox.example", "test1234")
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")
	Handler(false).ServeHTTP(rec, req)
	return rec
}

func TestReport(t *testing.T) {
	setupCalDAV(t, true, true)

	put := func(uid, dtstart, dtend string) {
		t.Helper()
		ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTART:" + dtstart + "\r\nDTEND:" + dtend + "\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
		path := "/mjl/calendars/default/" + url.PathEscape(uid) + ".ics"
		hdr := http.Header{}
		hdr.Set("Content-Type", "text/calendar")
		rec := objectReq(t, http.MethodPut, path, ics, hdr)
		if rec.Code != http.StatusCreated {
			t.Fatalf("put %s got %d body %q", uid, rec.Code, rec.Body.String())
		}
	}
	put("day1@mox", "20260815T090000Z", "20260815T100000Z")
	put("day2@mox", "20260820T090000Z", "20260820T100000Z")

	queryBody := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data/>
  </D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="2026-08-15T00:00:00Z" end="2026-08-16T00:00:00Z"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
	rec := report(t, "/mjl/calendars/default/", queryBody)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("calendar-query got %d body %q", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	if !strings.Contains(got, "calendar-data") {
		t.Fatalf("missing calendar-data: %q", got)
	}
	if !strings.Contains(got, "UID:day1@mox") {
		t.Fatalf("missing day1 ICS: %q", got)
	}
	if strings.Contains(got, "UID:day2@mox") {
		t.Fatalf("unexpected day2 ICS: %q", got)
	}
	if !strings.Contains(got, "/caldav/mjl/calendars/default/"+url.PathEscape("day1@mox")+".ics") {
		t.Fatalf("missing day1 href: %q", got)
	}

	known := "/caldav/mjl/calendars/default/" + url.PathEscape("day1@mox") + ".ics"
	unknown := "/caldav/mjl/calendars/default/" + url.PathEscape("missing@mox") + ".ics"
	multigetBody := `<?xml version="1.0" encoding="utf-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-data/>
  </D:prop>
  <D:href>` + known + `</D:href>
  <D:href>` + unknown + `</D:href>
</C:calendar-multiget>`
	rec = report(t, "/mjl/calendars/default/", multigetBody)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("calendar-multiget got %d body %q", rec.Code, rec.Body.String())
	}
	got = rec.Body.String()
	if !strings.Contains(got, "HTTP/1.1 200 OK") {
		t.Fatalf("missing 200: %q", got)
	}
	if !strings.Contains(got, "HTTP/1.1 404 Not Found") {
		t.Fatalf("missing 404: %q", got)
	}
	if !strings.Contains(got, "UID:day1@mox") {
		t.Fatalf("missing known ICS: %q", got)
	}
	if !strings.Contains(got, known) || !strings.Contains(got, unknown) {
		t.Fatalf("missing hrefs: %q", got)
	}
}
