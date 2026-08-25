# Calendar Phase 2 CalDAV Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the existing per-account calendar store over CalDAV (discovery, one calendar collection, PROPFIND/REPORT/GET/PUT/DELETE) behind `EnableCalDAV`, using HTTP Basic auth and the calendar domain service.

**Architecture:** Package `caldav` is an HTTP adapter only. Canonical data stays in `store.CalendarEvent`; `calendar.Service` gains UID-scoped object methods (`GetObject`, `PutObject`, `DeleteObject`, `ListObjects`) so a CalDAV resource is one UID (master plus exceptions) as one ICS document. Nested VEVENT subcomponents (VALARM) round-trip via the stored VEVENT ICS fragment, not via re-serialization from indexed fields.

**Tech Stack:** Go 1.25, `net/http`, `encoding/xml`, existing `store.OpenEmailAuth`, `webauth.ClientIP`, `icalendar.Parse`/`Serialize`, `calendar.Service`. No third-party CalDAV/WebDAV libraries.

## Global Constraints

- All existing IMAP- and SMTP-functions of mox remain working.
- Existing configuration files and their format remain unchanged. New settings exist only in the optional separate file `calendar.conf`.
- Absence of `calendar.conf` means: existing SMTP `FUTURERELEASE` and existing webmail functionality behave as before this expansion; the new calendar service, CalDAV endpoints and virtual `Scheduled` mailbox are disabled; startup and reload require no migration of existing config files.
- If `calendar.conf` is present but invalid, mox does not start and reports the config error; mox must never start with partially applied new settings.
- No existing field, grammar, default, file path or interpretation in the current mox configuration is changed to enable these features.
- Existing protocols keep their current capabilities, commands, mailboxes, ACL behaviour, error codes and delivery semantics.
- Canonical calendar data stays on the mox server. No IMAP mailbox or iCalendar text blob is a second authoritative copy. The calendar store is the source of truth; ICS text is a lossless encoding of unknown properties, not a competing store.
- Version 1 supports recurring events and exceptions, but not free/busy, scheduling assistant, shared calendars, tasks (`VTODO`), or server-side reminders.
- Version 1 can change or cancel a whole series or a single occurrence. The user action "this and all following" is absent.
- Writes require revision/ETag for optimistic concurrency.
- iCalendar input has limits for bytes, components, attendees, exceptions, recurrence complexity and expansion horizon.
- Calendar data falls under per-account limits from the new optional configuration.
- The full existing IMAP, SMTP, queue, webmail and config test suite runs unchanged as regression gate. Added features must not change existing golden protocol traces or existing configuration parse results.
- All existing configuration files are accepted identically and get identical behaviour when the new configuration is absent.
- An event or series remains equivalent in content after mox–CalDAV round-trips.
- Do not add third-party iCalendar or CalDAV libraries. Follow mox's pattern of first-party protocol packages.
- `CGO_ENABLED=0 go test` is the test command. Host often has no `go` on PATH; run via `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test …`. Do not run `./calendar` and `./store` in parallel (shared `testdata/store/data`); sequential or `-p 1` is safe.
- Cross-reference RFCs in comments as `// ../rfc/4791:section` (RFC 4791 is listed in `rfc/index.txt` as Roadmap and may not be fetched). Do not add a fetch-RFC task.

## Locked implementation details

### Feature gate

`mox.CalDAVEnabled() bool` returns `Conf.Calendar.EnableCalendar && Conf.Calendar.EnableCalDAV`.

Update `config.CalendarFile.EnableCalDAV` sconf-doc: drop the phrase `Unused in phase 1.` Keep `Requires EnableCalendar. Default: false.`

When CalDAV is disabled, `/caldav/` and `/.well-known/caldav` return 404. Handlers are still registered (restart-only config); they check the gate per request so tests can flip `mox.Conf.Calendar.EnableCalDAV`.

### URL layout (not configurable in mox.conf)

Derived paths only — no new Listener fields.

```
/.well-known/caldav                          → 301 Location: /caldav/
/caldav/                                     CalDAV root (OPTIONS; PROPFIND current-user-principal)
/caldav/{account}/                           principal
/caldav/{account}/calendars/                 calendar-home
/caldav/{account}/calendars/default/         one calendar collection
/caldav/{account}/calendars/default/{uid}.ics  calendar object
```

`{account}` is `store.Account.Name` (percent-decoded). `{uid}` is `url.PathEscape` of the iCalendar UID, plus suffix `.ics`. PUT filename UID must equal the UID inside the ICS (after PathUnescape, strip `.ics`); mismatch is HTTP 409.

After HTTP Basic auth, if `{account}` is present and does not equal the authenticated `acc.Name`, return 403.

### Auth

HTTP Basic, email address as username, same as webapi: `store.OpenEmailAuth`. Rate-limit failures with `webauth.ClientIP` + `mox.LimiterFailedAuth`. `WWW-Authenticate: Basic realm=caldav`. Login attempts recorded like webapi.

OPTIONS without credentials still returns DAV headers (clients probe). Every other method requires auth.

### DAV / CalDAV compliance (version 1 set)

Supported:

- `DAV` response header: `1, 3, calendar-access`
- `Allow`: `OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT`
- PROPFIND Depth 0 and 1. Depth infinity on a calendar collection is treated as Depth 1. Reject other depths with 400.
- Properties: `DAV:resourcetype`, `DAV:displayname`, `DAV:getetag`, `DAV:getcontenttype`, `DAV:current-user-principal`, `DAV:principal-URL`, `CALDAV:calendar-home-set`, `CALDAV:supported-calendar-component-set` (VEVENT only), `CS:getctag` (`http://calendarserver.org/ns/`)
- REPORT `{urn:ietf:params:xml:ns:caldav}calendar-query` with optional `time-range` (start/end as `xs:dateTime`). REPORT `{urn:ietf:params:xml:ns:caldav}calendar-multiget` with `DAV:href` list.
- PUT/DELETE If-Match on object ETag. If-None-Match: `*` on PUT means create-only (409 if UID exists). Missing If-Match on overwrite is allowed (clients vary); stale If-Match is 412.
- GET/HEAD `text/calendar; charset=utf-8`

Not in this plan: CalDAV scheduling (RFC 6638), MKCALENDAR, ACL, sync-collection REPORT, free-busy, VTODO, multiple calendars, well-known on a separately configured hostname.

### ETag and CTag

- `store.Calendar` gains `CTag int64` (default 0). Every successful Create, Update, Delete, Respond, PutObject, DeleteObject increments it by 1 inside the same write transaction.
- Collection `CS:getctag` and `DAV:getetag` on the calendar collection: `"ctag-{CTag}"`.
- Object ETag: `"rev-{max Revision among rows with that UID}"`.

### Calendar object mapping

One CalDAV resource = one UID. GET wraps stored VEVENT fragments (already `BEGIN:VEVENT`…`END:VEVENT`) in:

```
BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//mox//caldav//EN
…VEVENT fragments…
END:VCALENDAR
```

PUT parses a VCALENDAR. Reject `VTODO`. Count VEVENT components; if `> Limits().MaxComponents` return `calendar.ErrLimit`. All VEVENTs must share one UID. Nested VALARM stays inside the stored VEVENT ICS: persist `component.Serialize()` for each VEVENT, and extract `calendar.EventInput` only for indexes. Replacing a UID deletes existing rows for that UID then inserts the new set in one transaction.

`icalendar.Component` gains:

```go
func (c Component) Serialize() []byte
```

implemented by writing one component block (existing `writeComponentBlock`).

### HTTP wiring

Register on the same listeners/hosts as webmail, webaccount, and webapi (`accountHostMatch`):

- `ServiceHandle("caldav", accountHostMatch, "/caldav/", …)` with `http.StripPrefix("/caldav", caldav.Handler(forwarded))`
- `SystemHandle("caldav-wellknown", accountHostMatch, "/.well-known/caldav", redirect 301 to /caldav/)`

Do not add fields to `mox.conf` Listener structs.

### Package boundaries

| Package | Responsibility | Must not |
|---|---|---|
| `mox-` | `CalDAVEnabled()` | HTTP, ICS |
| `store` | `Calendar.CTag` | HTTP, ICS parse |
| `calendar` | object Get/Put/Delete/List, CTag bump | HTTP |
| `icalendar` | `Component.Serialize` | store, HTTP |
| `caldav` | HTTP CalDAV/WebDAV | IMAP, SMTP, sherpa |
| `http` | register paths | CalDAV XML |

### Out of this plan

iTIP/iMIP, webmail Agenda UI, ScheduledMessage, virtual IMAP `Scheduled`, backup/export docs, MKCALENDAR, RFC 6638 scheduling.

---

### Task 1: CalDAVEnabled and calendar CTag

**Files:**
- Modify: `mox-/calendar.go` — add `CalDAVEnabled`
- Modify: `mox-/calendar_test.go` — tests for the helper
- Modify: `config/calendar.go` — EnableCalDAV sconf-doc
- Modify: `store/calendar.go` — `CTag int64` on `Calendar`
- Modify: `calendar/service.go` — bump CTag on Create, Update, Delete, Respond
- Modify: `calendar/service_test.go` — assert CTag increments
- Test: `mox-/calendar_test.go`, `calendar/service_test.go`

**Interfaces:**
- Consumes: `mox.CalendarEnabled`, `store.Calendar`, existing `Service` mutations
- Produces: `func CalDAVEnabled() bool`; `store.Calendar.CTag int64`; CTag incremented on each successful mutation

- [ ] **Step 1: Write the failing test**

In `mox-/calendar_test.go` add:

```go
func TestCalDAVEnabledRequiresBothFlags(t *testing.T) {
    orig := Conf.Calendar
    t.Cleanup(func() { Conf.Calendar = orig })
    Conf.Calendar = config.CalendarFile{}
    if CalDAVEnabled() {
        t.Fatal("disabled")
    }
    Conf.Calendar.EnableCalDAV = true
    if CalDAVEnabled() {
        t.Fatal("caldav without calendar")
    }
    Conf.Calendar.EnableCalendar = true
    if !CalDAVEnabled() {
        t.Fatal("both true")
    }
}
```

In `calendar/service_test.go` add (using existing `testAccount`):

```go
func TestMutationsBumpCalendarCTag(t *testing.T) {
    acc := testAccount(t)
    start, end := testTimes()
    svc := Service{}
    ctag := func() int64 {
        var n int64
        acc.DB.Read(testctx, func(tx *bstore.Tx) error {
            c, err := acc.DefaultCalendar(tx)
            if err != nil {
                t.Fatal(err)
            }
            n = c.CTag
            return nil
        })
        return n
    }
    if ctag() != 0 {
        t.Fatalf("initial ctag %d", ctag())
    }
    ev, err := svc.Create(testctx, acc, EventInput{Summary: "A", Start: start, End: end})
    if err != nil || ctag() != 1 {
        t.Fatalf("create ctag=%d err=%v", ctag(), err)
    }
    _, err = svc.Update(testctx, acc, ev.ID, EventInput{UID: ev.UID, Revision: ev.Revision, Summary: "B", Start: start, End: end})
    if err != nil || ctag() != 2 {
        t.Fatalf("update ctag=%d err=%v", ctag(), err)
    }
    if err := svc.Delete(testctx, acc, ev.ID, ev.Revision+1, true); err != nil || ctag() != 3 {
        t.Fatalf("delete ctag=%d err=%v", ctag(), err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./mox- ./calendar -count=1 -run 'TestCalDAVEnabledRequiresBothFlags|TestMutationsBumpCalendarCTag'`

Expected: FAIL (`CalDAVEnabled` undefined; `CTag` undefined or stays 0).

- [ ] **Step 3: Write minimal implementation**

```go
func CalDAVEnabled() bool {
    return Conf.Calendar.EnableCalendar && Conf.Calendar.EnableCalDAV
}
```

Add `CTag int64` to `store.Calendar`. In each successful write path of Create/Update/Delete/Respond, after mutating events, load DefaultCalendar, increment CTag, `tx.Update`.

Update EnableCalDAV `sconf-doc` to: `Enable CalDAV. Requires EnableCalendar. Default: false.`

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./mox- ./calendar ./store ./config -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mox-/calendar.go mox-/calendar_test.go config/calendar.go store/calendar.go calendar/service.go calendar/service_test.go
git commit -m "feat: gate CalDAV and bump calendar CTag"
```

---

### Task 2: UID calendar objects and Component.Serialize

**Files:**
- Modify: `icalendar/object.go` — `Component.Serialize`
- Modify: `icalendar/object_test.go` — round-trip a VEVENT with VALARM
- Modify: `calendar/service.go` — `CalendarObject`, `GetObject`, `PutObject`, `DeleteObject`, `ListObjects`, `eventInputFromComponent`
- Modify: `calendar/service_test.go`
- Test: `icalendar/object_test.go`, `calendar/service_test.go`

**Interfaces:**
- Consumes: `icalendar.Parse`, `SplitSeries`, `ParseDateTime`, existing Create validation (`validateInput`, limits), `eventsByUID`
- Produces:

```go
type CalendarObject struct {
    UID   string
    ETag  string // `"rev-{maxRevision}"` including quotes
    ICS   []byte // full VCALENDAR
}

func (Service) GetObject(ctx context.Context, acc *store.Account, uid string) (CalendarObject, error)
func (Service) ListObjects(ctx context.Context, acc *store.Account) ([]CalendarObject, error)
func (Service) PutObject(ctx context.Context, acc *store.Account, uid string, ics []byte, ifMatch string) (CalendarObject, error)
func (Service) DeleteObject(ctx context.Context, acc *store.Account, uid string, ifMatch string) error
func (c Component) Serialize() []byte
```

`ifMatch` empty means unconditional. `ifMatch == "*"` on PutObject means fail with `ErrStale` if the UID already exists. Any other `ifMatch` must equal the current object ETag or `ErrStale`. Missing object: `ErrNotFound` (PutObject with empty/`*` ifMatch creates). PutObject bumps CTag once per call. DeleteObject bumps CTag.

`eventInputFromComponent` reads UID, RecurrenceID (serialized value string of the property if present), Sequence, Summary, Description, Location, Status, Class, Organizer, DTSTART (required), DTEND or DURATION (if neither and DateOnly, End=Start; if neither and not DateOnly, `ErrInvalid`), RRULE, RDATE values, EXDATE values, ATTENDEE (CN/ROLE/PARTSTAT/RSVP). Unknown properties: non-canonical names. Nested components are not copied into EventInput; they remain in `Component.Serialize()` stored as `ev.ICS`.

After building each `store.CalendarEvent` from input, set `ev.ICS = comp.Serialize()` (must start with `BEGIN:VEVENT` and include nested VALARM). Do not run `serializeEvent` for PutObject rows. Still enforce `len(ev.ICS) <= MaxICSBytes` per VEVENT fragment. Enforce MaxComponents on the number of VEVENT components. Enforce MaxAttendees / MaxExceptions / recurrence via existing `validateInput` per component. All VEVENTs must have the same UID equal to the `uid` argument; else `ErrInvalid`. Zero VEVENTs: `ErrInvalid`. Any VTODO (or other non-VEVENT/VTIMEZONE calendar components at VCALENDAR level besides ignored VTIMEZONE): `ErrInvalid`. Ignore VTIMEZONE components (times already resolved via TZID on properties).

GetObject/ListObjects wrap fragments with VCALENDAR VERSION 2.0 and PRODID `-//mox//caldav//EN`.

ListObjects returns one CalendarObject per distinct UID in the default calendar (masters and exceptions grouped). Sort by UID.

- [ ] **Step 1: Write the failing test**

```go
func TestComponentSerializeIncludesVALARM(t *testing.T) {
    raw := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:x@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:n\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
    cal, err := Parse(raw)
    if err != nil {
        t.Fatal(err)
    }
    got := cal.Components[0].Serialize()
    if !bytes.Contains(got, []byte("BEGIN:VALARM\r\n")) {
        t.Fatalf("%s", got)
    }
}

func TestPutGetObjectRoundTripVALARM(t *testing.T) {
    acc := testAccount(t)
    ics := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:obj1@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nSUMMARY:Hello\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:n\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
    svc := Service{}
    obj, err := svc.PutObject(testctx, acc, "obj1@mox", ics, "")
    if err != nil {
        t.Fatal(err)
    }
    if obj.ETag == "" || obj.UID != "obj1@mox" {
        t.Fatalf("%#v", obj)
    }
    got, err := svc.GetObject(testctx, acc, "obj1@mox")
    if err != nil {
        t.Fatal(err)
    }
    if !bytes.Contains(got.ICS, []byte("BEGIN:VALARM\r\n")) || !bytes.Contains(got.ICS, []byte("SUMMARY:Hello\r\n")) {
        t.Fatalf("%s", got.ICS)
    }
    _, err = svc.PutObject(testctx, acc, "obj1@mox", ics, `"rev-999"`)
    if !errors.Is(err, ErrStale) {
        t.Fatalf("got %v", err)
    }
    if err := svc.DeleteObject(testctx, acc, "obj1@mox", got.ETag); err != nil {
        t.Fatal(err)
    }
    if _, err := svc.GetObject(testctx, acc, "obj1@mox"); !errors.Is(err, ErrNotFound) {
        t.Fatalf("got %v", err)
    }
}

func TestPutObjectRejectsUIDMismatch(t *testing.T) {
    acc := testAccount(t)
    ics := []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:other@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
    _, err := Service{}.PutObject(testctx, acc, "obj1@mox", ics, "")
    if !errors.Is(err, ErrInvalid) {
        t.Fatalf("got %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./icalendar ./calendar -count=1 -run 'TestComponentSerializeIncludesVALARM|TestPutGetObjectRoundTripVALARM|TestPutObjectRejectsUIDMismatch'`

Expected: FAIL (methods missing).

- [ ] **Step 3: Write minimal implementation**

Implement `Serialize`, `eventInputFromComponent`, object methods as specified. PutObject: parse, validate, WithWLock + DB.Write, compare ifMatch against current ETag if rows exist, delete old UID rows, insert new, bump CTag once.

ETag helper:

```go
func objectETag(events []store.CalendarEvent) string {
    var max int64
    for _, ev := range events {
        if ev.Revision > max {
            max = ev.Revision
        }
    }
    return fmt.Sprintf(`"rev-%d"`, max)
}
```

New rows start at Revision 1. wrapICS as specified.

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./icalendar ./calendar -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add icalendar/object.go icalendar/object_test.go calendar/service.go calendar/service_test.go
git commit -m "feat: store calendar objects by UID for CalDAV"
```

---

### Task 3: CalDAV HTTP auth, OPTIONS, well-known, routing

**Files:**
- Create: `caldav/caldav.go` — `Handler`, mux on path, OPTIONS, 404 when disabled
- Create: `caldav/caldav_test.go`
- Test: `caldav/caldav_test.go`

**Interfaces:**
- Consumes: `mox.CalDAVEnabled`, `store.OpenEmailAuth`, `webauth.ClientIP`, `mox.LimiterFailedAuth`
- Produces: `func Handler(isForwarded bool) http.Handler`

Path handling: Handler is mounted at `/caldav` via StripPrefix in Task 6. Inside Handler, `r.URL.Path` is `""`, `/`, `/{account}`, `/{account}/calendars/`, `/{account}/calendars/default/`, `/{account}/calendars/default/{uid}.ics`.

For this task implement:

- If `!CalDAVEnabled()` → 404 for all methods.
- OPTIONS on any path: `DAV: 1, 3, calendar-access`, `Allow: OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT`, status 200, no body. Auth not required for OPTIONS.
- Other methods: require Basic auth. Missing/invalid → 401 + WWW-Authenticate. Auth success opens account; close after request.
- Authenticated GET `/` or `/` with empty path: 200 empty body is NOT required; return 403 or 404 for unimplemented resource types until Task 4. For this task, after auth, GET `/` returns 200 with `text/plain` body `caldav` so tests can see auth worked. Replace this stub in Task 4.
- Well-known redirect is registered in Task 6. In this package export:

```go
func WellKnown(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet && r.Method != http.MethodHead {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    if !mox.CalDAVEnabled() {
        http.NotFound(w, r)
        return
    }
    w.Header().Set("Location", "/caldav/")
    w.WriteHeader(http.StatusMovedPermanently)
}
```

Copy `testdata/webmail/mox.conf` and `testdata/webmail/domains.conf` to `testdata/caldav/`. Do not modify existing testdata configs. In `setupCalDAV`, load that config, `store.Init`, `OpenAccount(..., "mjl", false)`, then `acc.SetPassword(log, "test1234")` like `webapisrv/server_test.go`. HTTP Basic username `mjl@mox.example`, password `test1234`.

- [ ] **Step 1: Write the failing test**

```go
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
```

`setupCalDAV` loads testdata config, sets EnableCalendar/EnableCalDAV, store.Init, same pattern as `webmail/calendar_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav -count=1`

Expected: FAIL (package missing).

- [ ] **Step 3: Write minimal implementation**

Implement Handler and WellKnown. Copy auth flow from `webapisrv/server.go` Basic-auth block (LimiterFailedAuth, OpenEmailAuth, LoginAttemptAdd). Keep account in request context for later tasks:

```go
type ctxKey int
const accountCtxKey ctxKey = 1

func accountFrom(r *http.Request) *store.Account {
    acc, _ := r.Context().Value(accountCtxKey).(*store.Account)
    return acc
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add caldav testdata/caldav
git commit -m "feat: add CalDAV HTTP auth and OPTIONS"
```

---

### Task 4: PROPFIND principal, calendar-home, calendar collection

**Files:**
- Modify: `caldav/caldav.go` — routing by path
- Create: `caldav/xml.go` — multistatus encode
- Create: `caldav/propfind.go`
- Modify: `caldav/caldav_test.go`
- Test: `caldav/caldav_test.go`

**Interfaces:**
- Consumes: `accountFrom`, `acc.Name`, `calendar.Service.ListObjects` (for Depth 1 children hrefs on the default collection), `DefaultCalendar` display name
- Produces: PROPFIND multistatus XML

XML namespaces:

- `DAV:` → `D`
- `urn:ietf:params:xml:ns:caldav` → `C`
- `http://calendarserver.org/ns/` → `CS`

Use `encoding/xml`. Response `Content-Type: application/xml; charset=utf-8`. Status 207.

Path `{account}` must equal `acc.Name` else 403.

Resources:

1. `/` (root): `DAV:current-user-principal` href `/caldav/{account}/`
2. `/{account}/` principal: `DAV:resourcetype` includes `DAV:principal`; `DAV:principal-URL`; `CALDAV:calendar-home-set` href `/caldav/{account}/calendars/`
3. `/{account}/calendars/` collection: `DAV:resourcetype` collection; Depth 1 child `default/`
4. `/{account}/calendars/default/` calendar: `DAV:resourcetype` collection + `C:calendar`; `DAV:displayname` from `store.Calendar.Name`; `CS:getctag` and `DAV:getetag` from CTag; `CALDAV:supported-calendar-component-set` with `C:comp name="VEVENT"`; Depth 1 lists `…/default/{escapedUID}.ics` from ListObjects

Unknown requested properties: 404 propstat inside the response (standard WebDAV). Empty body PROPFIND = allprop of the properties listed in the locked compliance set.

Remove the Task 3 GET `/` stub. GET on collections without a file returns 200 empty or 403; lock: GET on a collection returns 200 with `text/xml` calendar description not required — return 200 `text/plain` empty. GET object is Task 5.

- [ ] **Step 1: Write the failing test**

Authenticated PROPFIND Depth 0 on `/` requesting `current-user-principal`; body contains `/caldav/mjl/` (account name from testdata is `mjl`).

PROPFIND Depth 0 on `/mjl/calendars/default/` requesting `getctag` and `resourcetype`; expect 207 and `calendar` resourcetype.

PROPFIND on `/otheruser/` as mjl → 403.

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav -count=1 -run PROPFIND`

Expected: FAIL (404/not 207).

- [ ] **Step 3: Write minimal implementation**

Implement XML types and PROPFIND. Parse Depth header default 0.

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add caldav
git commit -m "feat: CalDAV PROPFIND discovery and calendar collection"
```

---

### Task 5: GET PUT DELETE calendar objects with ETag

**Files:**
- Create: `caldav/object.go`
- Modify: `caldav/caldav.go` — dispatch GET/HEAD/PUT/DELETE
- Modify: `caldav/caldav_test.go`
- Test: `caldav/caldav_test.go`

**Interfaces:**
- Consumes: `calendar.Service{}.GetObject/PutObject/DeleteObject`
- Produces: HTTP mapping: `ErrNotFound` 404, `ErrStale` 412, `ErrInvalid` 403, `ErrLimit` 413, `ErrNotEnabled` 404, other errors 500

PUT path UID: `strings.TrimSuffix(url.PathUnescape(base), ".ics")`. Body limited to `Limits().MaxICSBytes` (read `http.MaxBytesReader`). Content-Type if present must contain `text/calendar` else 415; missing Content-Type is allowed.

GET/HEAD set ETag and Content-Type `text/calendar; charset=utf-8`. HEAD has no body.

If-Match / If-None-Match passed through to domain ifMatch as specified in Task 2 (`*` from If-None-Match).

- [ ] **Step 1: Write the failing test**

PUT a minimal VEVENT as `mjl` to `/mjl/calendars/default/obj1@mox.ics` (PathEscape the uid if needed — `@` can stay). GET returns the VEVENT. Second PUT with wrong If-Match → 412. DELETE with correct ETag → 204. GET → 404.

Also PUT UID mismatch filename vs ICS → 409 if domain returns ErrInvalid — map `ErrInvalid` on UID mismatch to 409. Lock: PutObject UID mismatch stays `ErrInvalid`; HTTP layer maps `ErrInvalid` to 409 for PUT and 403 for other methods. Implement by checking `strings.Contains(err.Error(), "UID")` or add `ErrConflict` — prefer wrapping: `fmt.Errorf("%w: UID mismatch", ErrInvalid)` and HTTP maps PUT+ErrInvalid to 409.

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav -count=1 -run 'TestPutGetDeleteObject'`

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

Dispatch in Handler. Status PUT: 201 if created, 204 if replaced (GetObject before put or PutObject returns whether existed — lock: GetObject first; if ErrNotFound then 201 else 204 after successful Put).

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav ./calendar -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add caldav calendar
git commit -m "feat: CalDAV GET PUT DELETE calendar objects"
```

---

### Task 6: REPORT calendar-query and calendar-multiget; HTTP listener wiring

**Files:**
- Create: `caldav/report.go`
- Modify: `caldav/caldav.go`
- Modify: `caldav/caldav_test.go`
- Modify: `http/web.go` — register `/caldav/` and `/.well-known/caldav` on webmail, account, and webapi listeners (same `accountHostMatch`)
- Modify: `http/web_test.go` — CalDAV 404 when gate off (default testdata has no calendar.conf)
- Modify: `config/doc.go` via `gendoc.sh` after updating sconf-doc (if Task 1 did not regenerate)
- Test: `caldav/caldav_test.go`, `http/web_test.go`

**Interfaces:**
- Consumes: `calendar.Service.Query` for time-range (map instances to UIDs, then GetObject each UID once); `GetObject` for multiget hrefs
- Produces: REPORT 207 multistatus with `C:calendar-data` containing ICS

calendar-query without time-range: all ListObjects. With `C:time-range` attributes `start` and/or `end` (RFC 3339): call `Service.Query` with missing start = far past `time.Time{}` replaced by `time.Date(1970,1,1,0,0,0,0,time.UTC)`, missing end = now+horizon via `time.Now().UTC().AddDate(0,0,Limits().ExpansionHorizonDays)`.

calendar-multiget: for each href, if object exists include 200+calendar-data, else 404 propstat.

Href in responses must be absolute-path `/caldav/{account}/calendars/default/{escaped}.ics`.

http/web.go: after each webmail/account/webapi ServiceHandle block, also:

```go
caldavHandler := mox.SafeHeaders(http.StripPrefix("/caldav", caldav.Handler(forwarded)))
srv.ServiceHandle("caldav", accountHostMatch, "/caldav/", caldavHandler)
srv.SystemHandle("caldav-wellknown", accountHostMatch, "/.well-known/caldav", mox.SafeHeaders(http.HandlerFunc(caldav.WellKnown)))
```

Use the same `forwarded` flag as that listener. Deduplicate if a single `ensureServe` port already registered caldav (registering twice is wrong). Helper `registerCalDAV(srv, accountHostMatch, forwarded bool)` with a map on `serve` or check existing ServiceHandlers names — simplest lock: call `registerCalDAV` once per `ensureServe` result using a `map[*serve]bool` local to the function that builds listeners.

In `http/web_test.go` existing TestServeHTTP: `GET /.well-known/caldav` on `http://mox.example/.well-known/caldav` expects 404 (CalDAV off). Do not change other assertions.

- [ ] **Step 1: Write the failing test**

In caldav tests: PUT two events on different days; REPORT calendar-query with time-range covering only one; expect one calendar-data. REPORT calendar-multiget with one known and one unknown href; 207 with 200 and 404.

In http/web_test.go add the 404 well-known assertion.

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav ./http -count=1 -run 'TestReport|TestServeHTTP'`

Expected: FAIL on REPORT; web_test may already 404 well-known (pass). If well-known already 404, keep the assertion as regression.

- [ ] **Step 3: Write minimal implementation**

REPORT parser + wiring helper.

Regenerate docs if EnableCalDAV comment changed: build `./mox` in podman then `./gendoc.sh`.

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./caldav ./calendar ./http ./webmail ./mox- -count=1`

Then: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test -p 1 ./imapserver ./smtpserver ./queue ./config -count=1`

Expected: PASS, existing web_test assertions unchanged.

- [ ] **Step 5: Commit**

```bash
git add caldav http/web.go http/web_test.go config/doc.go doc.go gendoc.sh
git commit -m "feat: CalDAV REPORT and HTTP listener wiring"
```

---

## Self-review

**Spec coverage (phase 2):**

| Spec item | Task |
|---|---|
| CalDAV discovery, principal, calendar-home, one collection | 4, 6 |
| PROPFIND, REPORT, GET, PUT, DELETE | 4, 5, 6 |
| Existing mox auth, TLS/hosts, account isolation | 3, 6 |
| EnableCalDAV gate; no mox.conf change | 1, 3, 6 |
| Round-trip event/series after CalDAV | 2, 5 |
| Limits including MaxComponents | 2 |
| ETag/revision concurrency | 1, 2, 5 |
| VTODO / extra calendars / scheduling / UI / iMIP | out of plan |

**Placeholder scan:** none; paths, ETag format, namespaces, status codes, testdata, podman test commands are concrete.

**Type consistency:** `calendar.CalendarObject` is the domain DTO; caldav HTTP maps it. `store.Calendar.CTag` is the collection tag. `mox.CalDAVEnabled` is the gate.
