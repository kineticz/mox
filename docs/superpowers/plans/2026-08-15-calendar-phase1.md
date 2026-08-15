# Calendar Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an optional `calendar.conf`, a reusable iCalendar engine, account-local calendar storage with one default calendar, a domain service for validated CRUD, and a webmail JSON API for time-window query, get, create, update, delete, and local participation-response.

**Architecture:** Canonical event data lives in the account bstore database. Package `icalendar` parses and serializes RFC 5545 text with unknown-property round-tripping. Package `calendar` is the domain service: it validates mutations, assigns revisions/ETags, expands recurrences for queries, and enforces limits. Webmail sherpa methods are a thin adapter. CalDAV, iTIP/iMIP, webmail UI, and scheduled send are out of scope for this plan.

**Tech Stack:** Go 1.25, `github.com/mjl-/sconf`, `github.com/mjl-/bstore`, `github.com/mjl-/sherpa`, existing `mox-` config loading, existing webmail auth.

## Global Constraints

- All existing IMAP- and SMTP-functions of mox remain working.
- Existing configuration files and their format remain unchanged. New settings exist only in the optional separate file `calendar.conf`.
- Absence of `calendar.conf` means: existing SMTP `FUTURERELEASE` and existing webmail functionality behave as before this expansion; the new calendar service, CalDAV endpoints and virtual `Scheduled` mailbox are disabled; startup and reload require no migration of existing config files.
- If `calendar.conf` is present but invalid, mox does not start and reports the config error; mox must never start with partially applied new settings.
- No existing field, grammar, default, file path or interpretation in the current mox configuration is changed to enable these features.
- Existing protocols keep their current capabilities, commands, mailboxes, ACL behaviour, error codes and delivery semantics.
- Canonical calendar data stays on the mox server. No IMAP mailbox or iCalendar text blob is a second authoritative copy. The calendar store is the source of truth; ICS text is a lossless encoding of unknown properties, not a competing store.
- Version 1 supports recurring events and exceptions, but not free/busy, scheduling assistant, shared calendars, tasks (`VTODO`), or server-side reminders.
- Version 1 can change or cancel a whole series or a single occurrence. The user action "this and all following" is absent. Valid incoming `RANGE=THISANDFUTURE` updates are processed for interoperability (iTIP, later phase); Phase 1 JSON API does not offer that action.
- Writes require revision/ETag for optimistic concurrency.
- iCalendar input has limits for bytes, components, attendees, exceptions, recurrence complexity and expansion horizon.
- Calendar data falls under per-account limits from the new optional configuration.
- The full existing IMAP, SMTP, queue, webmail and config test suite runs unchanged as regression gate. Added features must not change existing golden protocol traces or existing configuration parse results.
- All existing configuration files are accepted identically and get identical behaviour when the new configuration is absent.
- An event or series remains equivalent in content after mox–CalDAV round-trips (Phase 1: after parse→store→serialize round-trips).
- Cross-reference RFCs in comments the way existing packages do (`// ../rfc/5545:123`).
- Do not add third-party iCalendar libraries. Follow mox's pattern of first-party protocol packages (`message`, `smtp`, `dkim`).
- `CGO_ENABLED=0 go test` is the test command. Package tests use `testing`. Sherpa user errors use codes `user:error` / `user:badLogin` as existing webmail does.

## Locked implementation details (from spec § Gebonden implementatiedetails)

### `calendar.conf` sconf fields

File path: same directory as `mox.conf`, name exactly `calendar.conf`. Loaded once with static config (restart required). Not hot-reloaded.

```go
// config.CalendarFile is the parsed form of calendar.conf.
type CalendarFile struct {
    EnableCalendar  bool `sconf:"optional" sconf-doc:"Enable the server-side calendar store, calendar JSON API and default per-account calendar. Default: false."`
    EnableCalDAV    bool `sconf:"optional" sconf-doc:"Enable CalDAV. Requires EnableCalendar. Default: false. Unused in phase 1."`
    EnableIMIP      bool `sconf:"optional" sconf-doc:"Enable iTIP/iMIP scheduling over email. Requires EnableCalendar. Default: false. Unused in phase 1."`
    EnableScheduled bool `sconf:"optional" sconf-doc:"Enable the ScheduledMessage service and virtual IMAP Scheduled mailbox. Default: false. Unused in phase 1."`

    MaxICSBytes            int `sconf:"optional" sconf-doc:"Maximum iCalendar input size in bytes. Default: 1048576 (1 MiB)."`
    MaxComponents          int `sconf:"optional" sconf-doc:"Maximum VEVENT components per calendar object. Default: 64."`
    MaxAttendees           int `sconf:"optional" sconf-doc:"Maximum ATTENDEE properties per event. Default: 64."`
    MaxExceptions          int `sconf:"optional" sconf-doc:"Maximum exception instances (RECURRENCE-ID rows plus EXDATE values) per series. Default: 256."`
    MaxRecurrenceInstances int `sconf:"optional" sconf-doc:"Maximum expanded instances returned for one series in one query. Default: 520."`
    ExpansionHorizonDays   int `sconf:"optional" sconf-doc:"Maximum days a recurrence may be expanded from the query start. Default: 730."`
}
```

Zero-value defaults applied after parse when the field is 0:

| Field | Default |
|---|---|
| MaxICSBytes | 1048576 |
| MaxComponents | 64 |
| MaxAttendees | 64 |
| MaxExceptions | 256 |
| MaxRecurrenceInstances | 520 |
| ExpansionHorizonDays | 730 |

Config errors (mox does not start):

- File present but sconf-invalid.
- `EnableCalDAV` or `EnableIMIP` true while `EnableCalendar` is false.
- Any max/horizon field set to a negative value.

`mox.Config` gains field `Calendar config.CalendarFile` (zero if file absent). Helper `mox.CalendarEnabled() bool` returns `Conf.Calendar.EnableCalendar`.

`mox config describe-calendar` prints an annotated empty `calendar.conf`. `mox config describe-static` and `describe-domains` output must not change.

### Package boundaries

| Package | Responsibility | Must not |
|---|---|---|
| `config` | `CalendarFile` sconf struct only | parsing files, store, HTTP |
| `mox-` | load `calendar.conf`, expose `Conf.Calendar` | iCalendar syntax |
| `icalendar` | RFC 5545 parse, serialize, recurrence expansion | store, HTTP, mox config |
| `store` | `Calendar` and `CalendarEvent` types, `EnsureDefaultCalendar` | ICS parsing, HTTP |
| `calendar` | domain service: validate, mutate, query, limits | HTTP, IMAP, SMTP |
| `webmail` | sherpa adapter methods | recurrence math, ICS parser |

### Database indexes

See Task 7 types. Indexes:

- `CalendarEvent.UID` unique together with `RecurrenceID` (`unique UID+RecurrenceID`)
- `CalendarEvent.CalendarID` index
- `CalendarEvent.StartUTC` index (query window)
- `CalendarEvent.EndUTC` index
- `CalendarEvent.Revision` nonzero

### Quota defaults and expansion horizon

As in the table above. Query expansion stops at `min(queryEnd, queryStart + ExpansionHorizonDays)`. If a series would emit more than `MaxRecurrenceInstances` in that window, the query returns `user:error` with message `recurrence expansion exceeds limit`.

### Deferred to later plans

- IMAP UID strategy for virtual `Scheduled` mailbox (phase 5 / scheduled-send plan)
- CalDAV compliance features beyond the later CalDAV plan
- iTIP/iMIP outbox worker
- Webmail calendar UI

---

### Task 1: Optional calendar.conf loading

**Files:**
- Create: `config/calendar.go`
- Create: `mox-/calendar.go`
- Create: `mox-/calendar_test.go`
- Create: `testdata/calendar/mox.conf` (copy of `testdata/store/mox.conf`)
- Create: `testdata/calendar/domains.conf` (copy of `testdata/store/domains.conf`)
- Create: `testdata/calendar/calendar.conf`
- Create: `testdata/calendar/calendar-invalid.conf` (used by copying into place in tests)
- Modify: `mox-/config.go` — add `Calendar config.CalendarFile` on `Config`; after successful static+dynamic parse in `ParseConfig`, call calendar loader
- Modify: `main.go` — add `{"config describe-calendar", cmdConfigDescribeCalendar}` next to the other describe commands; implement `cmdConfigDescribeCalendar` mirroring `cmdConfigDescribeStatic` but describing `config.CalendarFile`

**Interfaces:**
- Consumes: existing `mox.ParseConfig`, `sconf.Parse` / `sconf.ParseFile`
- Produces: `config.CalendarFile`; `mox.Config.Calendar`; `func ApplyCalendarDefaults(c *config.CalendarFile)`; `func ValidateCalendarFile(c config.CalendarFile) error`; `func LoadCalendarConfig(dir string) (config.CalendarFile, bool, error)` where `bool` is whether the file existed; `func CalendarEnabled() bool`

- [ ] **Step 1: Write the failing test**

In `mox-/calendar_test.go`:

```go
package mox

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    "github.com/mjl-/mox/mlog"
)

func TestLoadCalendarConfigAbsent(t *testing.T) {
    dir := t.TempDir()
    cf, existed, err := LoadCalendarConfig(dir)
    if err != nil {
        t.Fatalf("absent file: %v", err)
    }
    if existed {
        t.Fatalf("existed=true for missing file")
    }
    if cf.EnableCalendar || cf.MaxICSBytes != 0 {
        t.Fatalf("zero value expected, got %#v", cf)
    }
}

func TestLoadCalendarConfigValid(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "calendar.conf")
    if err := os.WriteFile(p, []byte("EnableCalendar: true\nMaxAttendees: 10\n"), 0660); err != nil {
        t.Fatal(err)
    }
    cf, existed, err := LoadCalendarConfig(dir)
    if err != nil {
        t.Fatalf("valid file: %v", err)
    }
    if !existed || !cf.EnableCalendar || cf.MaxAttendees != 10 {
        t.Fatalf("got %#v existed=%v", cf, existed)
    }
    ApplyCalendarDefaults(&cf)
    if cf.MaxICSBytes != 1048576 || cf.ExpansionHorizonDays != 730 {
        t.Fatalf("defaults: %#v", cf)
    }
}

func TestLoadCalendarConfigInvalid(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "calendar.conf")
    if err := os.WriteFile(p, []byte("EnableCalendar: not-a-bool\n"), 0660); err != nil {
        t.Fatal(err)
    }
    _, existed, err := LoadCalendarConfig(dir)
    if err == nil || !existed {
        t.Fatalf("expected parse error, err=%v existed=%v", err, existed)
    }
}

func TestValidateCalendarFileCalDAVRequiresCalendar(t *testing.T) {
    err := ValidateCalendarFile(config.CalendarFile{EnableCalDAV: true})
    if err == nil {
        t.Fatalf("expected error")
    }
}

func TestParseConfigWithoutCalendarFileUnchanged(t *testing.T) {
    log := mlog.New("test", nil)
    c, errs := ParseConfig(context.Background(), log, filepath.FromSlash("../testdata/store/mox.conf"), true, false, false)
    if len(errs) != 0 {
        t.Fatalf("existing config without calendar.conf must still parse: %v", errs)
    }
    if c.Calendar.EnableCalendar {
        t.Fatalf("calendar must be disabled when calendar.conf is absent")
    }
}
```

Add `import "github.com/mjl-/mox/config"` as needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./mox- -run 'TestLoadCalendarConfig|TestValidateCalendarFile|TestParseConfigWithoutCalendarFile' -count=1`

Expected: FAIL with `LoadCalendarConfig` / `ValidateCalendarFile` undefined.

- [ ] **Step 3: Write minimal implementation**

`config/calendar.go`: `CalendarFile` struct exactly as locked above.

`mox-/calendar.go`:

```go
func LoadCalendarConfig(dir string) (config.CalendarFile, bool, error) {
    p := filepath.Join(dir, "calendar.conf")
    var cf config.CalendarFile
    err := sconf.ParseFile(p, &cf)
    if err != nil {
        if os.IsNotExist(err) {
            return config.CalendarFile{}, false, nil
        }
        return config.CalendarFile{}, true, fmt.Errorf("parsing %s: %w", p, err)
    }
    return cf, true, nil
}

func ApplyCalendarDefaults(c *config.CalendarFile) { /* zeros → defaults from table */ }

func ValidateCalendarFile(c config.CalendarFile) error {
    if (c.EnableCalDAV || c.EnableIMIP) && !c.EnableCalendar {
        return fmt.Errorf("%w: EnableCalDAV/EnableIMIP require EnableCalendar", ErrConfig)
    }
    if c.MaxICSBytes < 0 || c.MaxComponents < 0 || c.MaxAttendees < 0 || c.MaxExceptions < 0 || c.MaxRecurrenceInstances < 0 || c.ExpansionHorizonDays < 0 {
        return fmt.Errorf("%w: calendar limits must not be negative", ErrConfig)
    }
    return nil
}

func CalendarEnabled() bool { return Conf.Calendar.EnableCalendar }
```

In `ParseConfig`, after dynamic config parse (even if dynamic has errors, still attempt calendar parse so all errors are reported; if calendar file is invalid, append that error). Apply defaults then validate. Assign `c.Calendar`.

`cmdConfigDescribeCalendar`: `sconf.Describe(os.Stdout, &config.CalendarFile{})`.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./mox- -run 'TestLoadCalendarConfig|TestValidateCalendarFile|TestParseConfigWithoutCalendarFile' -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add config/calendar.go mox-/calendar.go mox-/calendar_test.go mox-/config.go main.go testdata/calendar
git commit -m "feat: load optional calendar.conf without changing mox.conf"
```

---

### Task 2: iCalendar content-line unfold and property parse

**Files:**
- Create: `icalendar/doc.go`
- Create: `icalendar/unfold.go`
- Create: `icalendar/property.go`
- Create: `icalendar/unfold_test.go`
- Create: `icalendar/property_test.go`

**Interfaces:**
- Consumes: none
- Produces:
  - `func Unfold(data []byte) ([]byte, error)` — join folded lines per RFC 5545; accept CR LF or LF; reject NULs
  - `type Param struct { Name string; Values []string }`
  - `type Property struct { Name string; Params []Param; Value string }`
  - `func ParseProperty(line []byte) (Property, error)`
  - `func (p Property) Param(name string) (string, bool)` — first value of named param, case-insensitive name
  - `func (p Property) WriteTo(w io.Writer) error` — serialize one property, fold at 75 octets with `\r\n `

- [ ] **Step 1: Write the failing test**

```go
func TestUnfoldFoldedLine(t *testing.T) {
    in := []byte("DESCRIPTION:This is a long\r\n  description\r\n")
    out, err := Unfold(in)
    if err != nil {
        t.Fatal(err)
    }
    if string(out) != "DESCRIPTION:This is a long description\r\n" {
        t.Fatalf("%q", out)
    }
}

func TestParsePropertyParamsAndEscapes(t *testing.T) {
    p, err := ParseProperty([]byte(`ATTENDEE;CN="Doe, Jane";RSVP=TRUE:mailto:jane@example.com`))
    if err != nil {
        t.Fatal(err)
    }
    if p.Name != "ATTENDEE" || p.Value != "mailto:jane@example.com" {
        t.Fatalf("%#v", p)
    }
    cn, ok := p.Param("cn")
    if !ok || cn != "Doe, Jane" {
        t.Fatalf("CN: %q %v", cn, ok)
    }
}
```

Also test: LF-only input unfolds; a line longer than 75 octets serializes with a fold; unknown property name is allowed (names are tokens).

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./icalendar -count=1`

Expected: FAIL, package or symbols missing.

- [ ] **Step 3: Write minimal implementation**

Unfold: scan bytes. After `\n`, if next is space or tab, drop the CRLF/LF and the one WSP (RFC 5545 folding). Keep other whitespace in values.

ParseProperty: split on first unquoted `:`. Left side is `name` plus `;param=value` or `param="quoted"`. Names uppercased on parse for matching; preserve original for serialize of unknown props? Lock: store `Name` as uppercase (IANA names are case-insensitive). Param names uppercase. Values unescaped for `VALUE` text: `\\` `\;` `\,` `\n`/`\N`.

Serialize: escape those in TEXT values only when writing known TEXT properties. For Task 2, `WriteTo` writes name, params, colon, value as stored (already unescaped in struct — re-escape TEXT). Add `func EscapeText(s string) string` and `func UnescapeText(s string) string`.

Fold: emit CRLF; if current line would exceed 75 octets, emit `\r\n ` and continue.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./icalendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add icalendar
git commit -m "feat: parse and fold iCalendar content lines"
```

---

### Task 3: VCALENDAR/VEVENT parse, serialize, unknown-property round-trip

**Files:**
- Create: `icalendar/object.go`
- Create: `icalendar/object_test.go`

**Interfaces:**
- Consumes: `Unfold`, `ParseProperty`, `Property.WriteTo`
- Produces:
  - `type Component struct { Name string; Properties []Property; Components []Component }`
  - `type Calendar struct { Properties []Property; Components []Component }`
  - `func Parse(data []byte) (Calendar, error)`
  - `func (c Calendar) Serialize() []byte`
  - `func (c Component) Prop(name string) (Property, bool)`
  - `func (c Component) Props(name string) []Property`

Limits are not enforced here (domain service does that). Parser rejects: missing `BEGIN:VCALENDAR`/`END:VCALENDAR`, mismatched BEGIN/END, data after end, empty name.

- [ ] **Step 1: Write the failing test**

```go
func TestParseSerializeRoundTripUnknownProperty(t *testing.T) {
    in := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//calendar//EN\r\nBEGIN:VEVENT\r\nUID:one@example.com\r\nDTSTAMP:20260101T120000Z\r\nDTSTART:20260102T090000Z\r\nDTEND:20260102T100000Z\r\nSUMMARY:Hello\r\nX-MOX-CUSTOM:keep-me\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
    cal, err := Parse(in)
    if err != nil {
        t.Fatal(err)
    }
    if len(cal.Components) != 1 || cal.Components[0].Name != "VEVENT" {
        t.Fatalf("%#v", cal.Components)
    }
    p, ok := cal.Components[0].Prop("X-MOX-CUSTOM")
    if !ok || p.Value != "keep-me" {
        t.Fatalf("unknown property: %#v", p)
    }
    out := cal.Serialize()
    cal2, err := Parse(out)
    if err != nil {
        t.Fatal(err)
    }
    p2, ok := cal2.Components[0].Prop("X-MOX-CUSTOM")
    if !ok || p2.Value != "keep-me" {
        t.Fatalf("round-trip lost custom property: %#v", p2)
    }
    if sum, _ := cal2.Components[0].Prop("SUMMARY"); sum.Value != "Hello" {
        t.Fatalf("summary %q", sum.Value)
    }
}
```

Also: nested `VALARM` is preserved as a subcomponent (even though reminders are a non-goal, round-trip must not drop unknown/unhandled components). `VTODO` at top level is preserved as a component but calendar domain service will reject it later.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./icalendar -run TestParseSerializeRoundTripUnknownProperty -count=1`

Expected: FAIL, `Parse` undefined.

- [ ] **Step 3: Write minimal implementation**

Stack-based BEGIN/END parser over unfolded lines. Serialize writes `BEGIN:VCALENDAR`, calendar properties, components recursively, `END:VCALENDAR`, all CRLF, folding via `Property.WriteTo`.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./icalendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add icalendar
git commit -m "feat: parse and serialize VCALENDAR with property round-trip"
```

---

### Task 4: DATE, DATE-TIME, TZID as UTC instants

**Files:**
- Create: `icalendar/datetime.go`
- Create: `icalendar/datetime_test.go`

**Interfaces:**
- Consumes: `Property`
- Produces:
  - `type DateTime struct { UTC time.Time; TZID string; DateOnly bool; Floating bool }`
  - `func ParseDateTime(p Property) (DateTime, error)` — reads VALUE=DATE vs DATE-TIME; TZID param; trailing `Z` means UTC
  - `func (d DateTime) Property(name string) Property` — inverse

Floating local time (no TZID, no Z) is stored with `Floating: true` and interpreted in UTC for indexing only when TZID is empty and not Z — lock: treat floating as UTC for comparison/index, keep `Floating` so serialize omits Z and TZID. This matches "original representation preserved".

- [ ] **Step 1: Write the failing test**

```go
func TestParseDateTimeUTC(t *testing.T) {
    p := Property{Name: "DTSTART", Value: "20260815T090000Z"}
    dt, err := ParseDateTime(p)
    if err != nil {
        t.Fatal(err)
    }
    if dt.DateOnly || dt.TZID != "" || dt.UTC.UTC() != time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC) {
        t.Fatalf("%#v", dt)
    }
}

func TestParseDateTimeBerlinDST(t *testing.T) {
    p := Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20260329T090000"}
    dt, err := ParseDateTime(p)
    if err != nil {
        t.Fatal(err)
    }
    // Europe/Berlin 2026-03-29 09:00 is CEST (UTC+2).
    if dt.TZID != "Europe/Berlin" || dt.UTC.UTC() != time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC) {
        t.Fatalf("%#v %s", dt, dt.UTC)
    }
}

func TestParseDateOnly(t *testing.T) {
    p := Property{Name: "DTSTART", Params: []Param{{Name: "VALUE", Values: []string{"DATE"}}}, Value: "20260101"}
    dt, err := ParseDateTime(p)
    if err != nil {
        t.Fatal(err)
    }
    if !dt.DateOnly || !dt.UTC.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
        t.Fatalf("%#v", dt)
    }
}

func TestDateTimeSerializePreservesTZID(t *testing.T) {
    loc, err := time.LoadLocation("Europe/Berlin")
    if err != nil {
        t.Fatal(err)
    }
    d := DateTime{UTC: time.Date(2026, 1, 12, 8, 0, 0, 0, time.UTC), TZID: "Europe/Berlin"}
    p := d.Property("DTSTART")
    tz, _ := p.Param("TZID")
    if tz != "Europe/Berlin" || p.Value != "20260112T090000" {
        t.Fatalf("%#v", p)
    }
    _ = loc
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./icalendar -run 'TestParseDateTime|TestParseDateOnly|TestDateTimeSerialize' -count=1`

Expected: FAIL, `ParseDateTime` undefined.

- [ ] **Step 3: Write minimal implementation**

Use `time.LoadLocation(tzid)` for IANA TZIDs. Unknown TZID is an error. DATE values are UTC midnight of that calendar date. `Property` emit: DateOnly → `VALUE=DATE` and `YYYYMMDD`; TZID → param and local clock in that zone; UTC → value ending in `Z`.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./icalendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add icalendar
git commit -m "feat: interpret iCalendar DATE-TIME in IANA time zones"
```

---

### Task 5: RRULE, RDATE, EXDATE expansion

**Files:**
- Create: `icalendar/recur.go`
- Create: `icalendar/recur_test.go`

**Interfaces:**
- Consumes: `DateTime`, `Component`
- Produces:
  - `type Recurrence struct { Freq string; Interval int; Count int; Until *time.Time; ByDay []WeekdayNum; ByMonthDay []int; UntilDateOnly bool }`
  - `type WeekdayNum struct { Offset int; Day time.Weekday }` // Offset 0 means every that weekday; -1 last
  - `func ParseRRULE(value string) (Recurrence, error)`
  - `func (c Component) RecurrenceSet() (rrule Recurrence, rdates, exdates []DateTime, err error)`
  - `func Expand(dtstart DateTime, r Recurrence, rdates, exdates []DateTime, from, to time.Time, max int) (instances []DateTime, err error)`
  - `var ErrExpansionLimit = errors.New("recurrence expansion exceeds limit")`

`Expand` emits instance starts in `[from, to)` (half-open), including `dtstart` if in range, plus RDATEs, minus EXDATEs matched by UTC instant (date-only EXDATE matches that date). If more than `max` would be emitted, return `ErrExpansionLimit`. Do not emit beyond `to`. Freq supported in v1: `DAILY`, `WEEKLY`, `MONTHLY`, `YEARLY`. Interval default 1. BYDAY for WEEKLY and MONTHLY (including `1MO` and `-1FR`). BYMONTHDAY for MONTHLY/YEARLY. Unsupported parts (`BYHOUR`, `BYSETPOS`, `WKST` other than MO, `UNTIL`+`COUNT` together) return a parse error.

- [ ] **Step 1: Write the failing test**

```go
func TestExpandWeeklySkipsDSTGapAndKeepsLocalHour(t *testing.T) {
    start, err := ParseDateTime(Property{Name: "DTSTART", Params: []Param{{Name: "TZID", Values: []string{"Europe/Berlin"}}}, Value: "20260323T090000"})
    if err != nil {
        t.Fatal(err)
    }
    r, err := ParseRRULE("FREQ=WEEKLY;BYDAY=MO;COUNT=3")
    if err != nil {
        t.Fatal(err)
    }
    from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
    to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
    inst, err := Expand(start, r, nil, nil, from, to, 520)
    if err != nil {
        t.Fatal(err)
    }
    if len(inst) != 3 {
        t.Fatalf("len=%d", len(inst))
    }
    // Mondays 23 Mar, 30 Mar, 6 Apr 2026 at 09:00 Europe/Berlin.
    // 29 Mar 2026 is DST start; 30 Mar is still 09:00 local (07:00 UTC).
    want := []time.Time{
        time.Date(2026, 3, 23, 8, 0, 0, 0, time.UTC),
        time.Date(2026, 3, 30, 7, 0, 0, 0, time.UTC),
        time.Date(2026, 4, 6, 7, 0, 0, 0, time.UTC),
    }
    for i := range want {
        if !inst[i].UTC.Equal(want[i]) {
            t.Fatalf("inst[%d]=%s want %s", i, inst[i].UTC, want[i])
        }
        if inst[i].TZID != "Europe/Berlin" {
            t.Fatalf("tz %q", inst[i].TZID)
        }
    }
}

func TestExpandExdateRemovesOccurrence(t *testing.T) {
    start := DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)} // Monday
    r, err := ParseRRULE("FREQ=WEEKLY;COUNT=3")
    if err != nil {
        t.Fatal(err)
    }
    ex := []DateTime{{UTC: time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)}}
    inst, err := Expand(start, r, nil, ex, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 520)
    if err != nil {
        t.Fatal(err)
    }
    if len(inst) != 2 || !inst[0].UTC.Equal(start.UTC) || !inst[1].UTC.Equal(time.Date(2026, 1, 19, 9, 0, 0, 0, time.UTC)) {
        t.Fatalf("%v", inst)
    }
}

func TestExpandMonthlyByDayLastFriday(t *testing.T) {
    start := DateTime{UTC: time.Date(2026, 1, 30, 15, 0, 0, 0, time.UTC), TZID: "UTC"}
    r, err := ParseRRULE("FREQ=MONTHLY;BYDAY=-1FR;COUNT=2")
    if err != nil {
        t.Fatal(err)
    }
    inst, err := Expand(start, r, nil, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 520)
    if err != nil {
        t.Fatal(err)
    }
    if len(inst) != 2 {
        t.Fatalf("len=%d %#v", len(inst), inst)
    }
}

func TestExpandHitsMax(t *testing.T) {
    start := DateTime{UTC: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
    r, err := ParseRRULE("FREQ=DAILY")
    if err != nil {
        t.Fatal(err)
    }
    _, err = Expand(start, r, nil, nil, start.UTC, start.UTC.Add(100*24*time.Hour), 10)
    if !errors.Is(err, ErrExpansionLimit) {
        t.Fatalf("got %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./icalendar -run TestExpand -count=1`

Expected: FAIL, `Expand` undefined.

- [ ] **Step 3: Write minimal implementation**

Iterate in the event's TZID location so "every Monday 09:00 Europe/Berlin" stays 09:00 across DST. For MONTHLY BYDAY with offset, find the n-th weekday of the month. EXDATE: map of UTC Unix seconds (and date-only YYYYMMDD keys).

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./icalendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add icalendar
git commit -m "feat: expand RRULE with DST-stable local times"
```

---

### Task 6: RECURRENCE-ID exceptions in a series

**Files:**
- Modify: `icalendar/object.go` (helpers)
- Create: `icalendar/exception.go`
- Create: `icalendar/exception_test.go`

**Interfaces:**
- Consumes: `Component`, `ParseDateTime`, `Expand`
- Produces:
  - `func (c Component) UID() string`
  - `func (c Component) RecurrenceID() (DateTime, bool, error)`
  - `func (c Component) Sequence() int` — 0 if absent
  - `func SplitSeries(comps []Component) (masters []Component, exceptions map[string][]Component, err error)` keyed by UID

A master is a VEVENT with UID and no RECURRENCE-ID. An exception has UID + RECURRENCE-ID. `RANGE=THISANDFUTURE` on RECURRENCE-ID is parsed and stored as param on the property; Phase 1 domain service rejects creating such events via JSON API, but the parser keeps the param for later iTIP.

- [ ] **Step 1: Write the failing test**

```go
func TestSplitSeriesMasterAndException(t *testing.T) {
    cal, err := Parse([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:s@example.com\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260105T090000Z\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:Series\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:s@example.com\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260112T110000Z\r\nRECURRENCE-ID:20260112T090000Z\r\nSUMMARY:Moved\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
    if err != nil {
        t.Fatal(err)
    }
    masters, ex, err := SplitSeries(cal.Components)
    if err != nil {
        t.Fatal(err)
    }
    if len(masters) != 1 || len(ex["s@example.com"]) != 1 {
        t.Fatalf("masters=%d ex=%d", len(masters), len(ex["s@example.com"]))
    }
    rid, ok, err := ex["s@example.com"][0].RecurrenceID()
    if err != nil || !ok || !rid.UTC.Equal(time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)) {
        t.Fatalf("%v %v %v", rid, ok, err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./icalendar -run TestSplitSeriesMasterAndException -count=1`

Expected: FAIL, `SplitSeries` undefined.

- [ ] **Step 3: Write minimal implementation**

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./icalendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add icalendar
git commit -m "feat: split VEVENT series into master and exceptions"
```

---

### Task 7: Account calendar store and default calendar

**Files:**
- Create: `store/calendar.go`
- Create: `store/calendar_test.go`
- Modify: `store/account.go` — append types to `DBTypes`; in `OpenAccountDB` after the existing singleton ensure-block, call `ensureDefaultCalendar(tx)` only when `mox.CalendarEnabled()`

**Interfaces:**
- Consumes: `mox.CalendarEnabled()`
- Produces:

```go
type Calendar struct {
    ID   int64
    Name string `bstore:"nonzero"` // "Calendar"
    UID  string `bstore:"nonzero,unique"` // stable collection UID, generated once
}

type CalendarAttendee struct {
    Address  string // mailto without prefix, canonical
    Name     string
    Role     string // REQ-PARTICIPANT default
    PartStat string // NEEDS-ACTION, ACCEPTED, TENTATIVE, DECLINED
    RSVP     bool
}

type CalendarEvent struct {
    ID           int64
    CalendarID   int64     `bstore:"ref Calendar,index"`
    UID          string    `bstore:"nonzero,unique UID+RecurrenceID"`
    RecurrenceID string    // empty for master; RFC5545 RECURRENCE-ID value form
    Sequence     int
    Revision     int64     `bstore:"nonzero"`
    Created      time.Time `bstore:"default now"`
    LastModified time.Time `bstore:"default now"`
    Organizer    string
    Summary      string
    Description  string
    Location     string
    Status       string // CONFIRMED/TENTATIVE/CANCELLED
    Class        string // PUBLIC/PRIVATE/CONFIDENTIAL
    StartUTC     time.Time `bstore:"index"`
    EndUTC       time.Time `bstore:"index"`
    TZID         string
    DateOnly     bool
    Floating     bool
    RRULE        string
    RDATE        []string
    EXDATE       []string
    Attendees    []CalendarAttendee
    ICS          []byte `bstore:"nonzero"` // serialized VEVENT component including unknown properties
}

func (a *Account) DefaultCalendar(tx *bstore.Tx) (Calendar, error)
func ensureDefaultCalendar(tx *bstore.Tx) error // insert one Calendar{Name:"Calendar"} if none
```

`ensureDefaultCalendar` is a no-op when calendar is disabled so existing tests without `calendar.conf` never insert calendar rows.

When calendar is enabled and an old account DB is opened, `bstore.Open` with the new types migrates empty tables; then `ensureDefaultCalendar` inserts the default calendar. Mail records are untouched.

- [ ] **Step 1: Write the failing test**

Follow `store/account_test.go` setup (`mox.ConfigStaticPath` testdata/store, `store.Init`, `OpenAccount`). Enable calendar for the test by setting `mox.Conf.Calendar.EnableCalendar = true` after `MustLoadConfig`.

```go
func TestEnsureDefaultCalendar(t *testing.T) {
    // setup identical to TestAccount in account_test.go, then:
    mox.Conf.Calendar.EnableCalendar = true
    acc, err := OpenAccount(mlog.New("store", nil), "mjl", false)
    // ...
    var n int
    err = acc.DB.Read(ctxbg, func(tx *bstore.Tx) error {
        list, err := bstore.QueryTx[Calendar](tx).List()
        n = len(list)
        if n == 1 && list[0].Name != "Calendar" {
            t.Fatalf("%#v", list[0])
        }
        return err
    })
    if n != 1 {
        t.Fatalf("default calendar count %d", n)
    }
}

func TestNoCalendarWhenDisabled(t *testing.T) {
    // MustLoadConfig testdata/store (no calendar.conf)
    acc, err := OpenAccount(...)
    err = acc.DB.Read(ctxbg, func(tx *bstore.Tx) error {
        list, err := bstore.QueryTx[Calendar](tx).List()
        if len(list) != 0 {
            t.Fatalf("unexpected calendars %d", len(list))
        }
        return err
    })
}
```

Reuse the existing account_test helper patterns (`tcheck`, testdata cleanup). Do not change assertions of existing store tests.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./store -run 'TestEnsureDefaultCalendar|TestNoCalendarWhenDisabled' -count=1`

Expected: FAIL, `Calendar` type or `ensureDefaultCalendar` missing.

- [ ] **Step 3: Write minimal implementation**

Generate `Calendar.UID` with `fmt.Sprintf("calendar-%s", random hex 16 bytes)` once.

Call `ensureDefaultCalendar` from the `db.Write` block in `OpenAccountDB` that already ensures Settings/DiskUsage, guarded by `mox.CalendarEnabled()`. For brand-new accounts (`initAccount`), also insert the default calendar when enabled.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./store -run 'TestEnsureDefaultCalendar|TestNoCalendarWhenDisabled|TestAccount' -count=1`

Expected: PASS. Existing `TestAccount` still passes.

- [ ] **Step 5: Commit**

```bash
git add store/calendar.go store/calendar_test.go store/account.go
git commit -m "feat: store one default calendar per account"
```

---

### Task 8: Domain service create/update/delete with revision and limits

**Files:**
- Create: `calendar/doc.go`
- Create: `calendar/service.go`
- Create: `calendar/limits.go`
- Create: `calendar/service_test.go`

**Interfaces:**
- Consumes: `store.CalendarEvent`, `store.Calendar`, `icalendar.Parse` / `Serialize` / `ParseDateTime` / `Expand`, `mox.Conf.Calendar` limits
- Produces:

```go
var (
    ErrNotEnabled   = errors.New("calendar not enabled")
    ErrNotFound     = errors.New("event not found")
    ErrStale        = errors.New("stale revision")
    ErrLimit        = errors.New("calendar limit exceeded")
    ErrInvalid      = errors.New("invalid event")
)

type EventInput struct {
    UID          string // empty → generate UUID-like UID `@mox`
    RecurrenceID string // empty for master; set to change one occurrence
    Sequence     int
    Revision     int64  // required nonzero on update/delete
    Summary      string
    Description  string
    Location     string
    Status       string
    Class        string
    Organizer    string
    Start        icalendar.DateTime
    End          icalendar.DateTime
    RRULE        string
    RDATE        []string
    EXDATE       []string
    Attendees    []store.CalendarAttendee
    Unknown      []icalendar.Property // extra VEVENT properties to keep
}

type Service struct{} // empty; methods use acc + limits from config

func Limits() config.CalendarFile // Conf.Calendar after defaults

func (Service) Create(ctx context.Context, acc *store.Account, in EventInput) (store.CalendarEvent, error)
func (Service) Update(ctx context.Context, acc *store.Account, id int64, in EventInput) (store.CalendarEvent, error)
func (Service) Delete(ctx context.Context, acc *store.Account, id int64, revision int64, wholeSeries bool) error
func (Service) Get(ctx context.Context, acc *store.Account, id int64) (store.CalendarEvent, error)
```

Rules:

- If `!mox.CalendarEnabled()`, every method returns `ErrNotEnabled`.
- Create: generate UID if empty; Revision starts at 1; Sequence defaults to 0; require End after Start (date-only: End ≥ Start); reject `VTODO`; reject attendee count > MaxAttendees; reject ICS size > MaxICSBytes after serialize; reject exception count (len(EXDATE)+existing exception rows) > MaxExceptions; reject RRULE parse errors; `THISANDFUTURE` in input is `ErrInvalid`.
- Update: load by id; if `in.Revision != ev.Revision` return `ErrStale`; increment Revision; bump Sequence by +1 if any scheduling-visible field changed (summary, dtstart/dtend, rrule, attendees, status, recurrence-id). Changing only Description/Location still bumps Revision but Sequence only if those are included in iTIP later — lock for v1: Sequence increments on any Update.
- Delete with `wholeSeries=false` on a master with RRULE: add EXDATE for that instance instead? JSON delete of a master without RecurrenceID deletes the whole series. Delete of an exception row deletes only that exception (restores the master occurrence unless EXDATE is also set). `wholeSeries=true` deletes master + all exception rows with that UID.
- Create of an exception: RecurrenceID set, UID must already exist as master.
- Persist `ICS` as serialized VEVENT component (`BEGIN:VEVENT` ... `END:VEVENT`) including Unknown properties.
- Do not send mail. Do not write outbox rows.

- [ ] **Step 1: Write the failing test**

Use store test setup with `mox.Conf.Calendar.EnableCalendar = true` and `ApplyCalendarDefaults`.

```go
func TestCreateGetUpdateStaleDelete(t *testing.T) {
    // open account with calendar enabled
    svc := calendar.Service{}
    ev, err := svc.Create(ctx, acc, calendar.EventInput{
        Summary: "Meet",
        Start:   icalendar.DateTime{UTC: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)},
        End:     icalendar.DateTime{UTC: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)},
    })
    if err != nil {
        t.Fatal(err)
    }
    if ev.Revision != 1 || ev.UID == "" || ev.Summary != "Meet" {
        t.Fatalf("%#v", ev)
    }
    got, err := svc.Get(ctx, acc, ev.ID)
    if err != nil || got.Summary != "Meet" {
        t.Fatalf("%v %#v", err, got)
    }
    _, err = svc.Update(ctx, acc, ev.ID, calendar.EventInput{Revision: 0, Summary: "Nope", Start: evStart, End: evEnd})
    if !errors.Is(err, calendar.ErrStale) {
        t.Fatalf("stale: %v", err)
    }
    up, err := svc.Update(ctx, acc, ev.ID, calendar.EventInput{Revision: ev.Revision, Summary: "Meet 2", Start: ..., End: ...})
    if err != nil || up.Revision != 2 || up.Sequence != 1 {
        t.Fatalf("%#v %v", up, err)
    }
    if err := svc.Delete(ctx, acc, ev.ID, up.Revision, true); err != nil {
        t.Fatal(err)
    }
    if _, err := svc.Get(ctx, acc, ev.ID); !errors.Is(err, calendar.ErrNotFound) {
        t.Fatalf("got %v", err)
    }
}

func TestCreateDisabled(t *testing.T) {
    mox.Conf.Calendar.EnableCalendar = false
    _, err := calendar.Service{}.Create(ctx, acc, calendar.EventInput{Summary: "x", Start: s, End: e})
    if !errors.Is(err, calendar.ErrNotEnabled) {
        t.Fatalf("%v", err)
    }
}

func TestCreateAttendeeLimit(t *testing.T) {
    mox.Conf.Calendar.EnableCalendar = true
    mox.Conf.Calendar.MaxAttendees = 1
    _, err := calendar.Service{}.Create(ctx, acc, calendar.EventInput{
        Summary: "x", Start: s, End: e,
        Attendees: []store.CalendarAttendee{{Address: "a@e.com"}, {Address: "b@e.com"}},
    })
    if !errors.Is(err, calendar.ErrLimit) {
        t.Fatalf("%v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./calendar -count=1`

Expected: FAIL, package missing.

- [ ] **Step 3: Write minimal implementation**

Hold `acc` write lock around DB writes that mutate events (same discipline as mailbox mutations, even without IMAP broadcast yet).

Build a `icalendar.Component{Name:"VEVENT"}` from EventInput + Unknown, serialize to `ICS`.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./calendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add calendar
git commit -m "feat: calendar domain service with optimistic concurrency"
```

---

### Task 9: Query events in a time window

**Files:**
- Create: `calendar/query.go`
- Create: `calendar/query_test.go`
- Modify: `calendar/service.go` — add `Query` method

**Interfaces:**
- Consumes: `Expand`, `store.CalendarEvent`
- Produces:

```go
type Instance struct {
    Event      store.CalendarEvent
    Start      icalendar.DateTime
    End        icalendar.DateTime
    Exception  bool // true if this instance is an exception row
}

func (Service) Query(ctx context.Context, acc *store.Account, from, to time.Time) ([]Instance, error)
```

Algorithm:

1. If disabled, `ErrNotEnabled`.
2. Cap `to` at `from + ExpansionHorizonDays`.
3. Load default calendar.
4. Load all events for that calendar where `RecurrenceID==""` (masters) and (`RRULE==""` and overlap `StartUTC/EndUTC` with `[from,to)` OR `RRULE!=""`).
5. For non-recurring: include if `StartUTC < to && EndUTC > from`.
6. For recurring: `Expand` with max `MaxRecurrenceInstances`; duration = master.EndUTC - master.StartUTC; skip instances whose RecurrenceID exists as exception row; add exception rows that overlap the window.
7. Sort by Start.UTC then UID.

- [ ] **Step 1: Write the failing test**

```go
func TestQueryWindowIncludesRecurringInstance(t *testing.T) {
    svc := calendar.Service{}
    _, err := svc.Create(ctx, acc, calendar.EventInput{
        Summary: "Standup",
        Start:   icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)},
        End:     icalendar.DateTime{UTC: time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)},
        RRULE:   "FREQ=WEEKLY;COUNT=5",
    })
    if err != nil {
        t.Fatal(err)
    }
    inst, err := svc.Query(ctx, acc, time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC))
    if err != nil {
        t.Fatal(err)
    }
    if len(inst) != 1 || inst[0].Start.UTC != time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC) {
        t.Fatalf("%#v", inst)
    }
}

func TestQueryExceptionReplacesOccurrence(t *testing.T) {
    // create weekly series, then Create exception with same UID and RecurrenceID of second instance, new start
    // query that day: one instance at the new time, Exception true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./calendar -run TestQuery -count=1`

Expected: FAIL, `Query` undefined.

- [ ] **Step 3: Write minimal implementation**

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./calendar -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add calendar
git commit -m "feat: query calendar instances in a time window"
```

---

### Task 10: Webmail JSON API adapter

**Files:**
- Create: `webmail/calendar.go`
- Create: `webmail/calendar_test.go`
- Modify: `webmail/api.go` only if a type must be shared; prefer keeping types in `webmail/calendar.go`
- Modify: `webmail/api.json` by running `./genapidoc.sh` (do not hand-edit)
- After api.json change, run `./gents.sh webmail/api.json webmail/api.ts` as Makefile `build0` does

**Interfaces:**
- Consumes: `calendar.Service` methods, webmail `requestInfo` auth
- Produces: sherpa methods on `Webmail`:

```go
type CalendarTime struct {
    Instant  time.Time
    TZID     string
    DateOnly bool
}

type CalendarAttendeeView struct {
    Address  string
    Name     string
    Role     string
    PartStat string
}

type CalendarEventView struct {
    ID           int64
    UID          string
    RecurrenceID string
    Sequence     int
    Revision     int64
    Summary      string
    Description  string
    Location     string
    Status       string
    Organizer    string
    Start        CalendarTime
    End          CalendarTime
    RRULE        string
    RDATE        []string
    EXDATE       []string
    Attendees    []CalendarAttendeeView
    InstanceStart *CalendarTime // set in query results for expanded instances
}

func (w Webmail) CalendarEnabled(ctx context.Context) bool
func (w Webmail) CalendarQuery(ctx context.Context, from, to time.Time) []CalendarEventView
func (w Webmail) CalendarGet(ctx context.Context, id int64) CalendarEventView
func (w Webmail) CalendarCreate(ctx context.Context, e CalendarEventView) CalendarEventView
func (w Webmail) CalendarUpdate(ctx context.Context, e CalendarEventView) CalendarEventView
func (w Webmail) CalendarDelete(ctx context.Context, id int64, revision int64, wholeSeries bool)
func (w Webmail) CalendarRespond(ctx context.Context, id int64, revision int64, partStat string) CalendarEventView
```

`CalendarRespond` updates only the attendee matching the logged-in address (`reqInfo.LoginAddress`) to `ACCEPTED`/`TENTATIVE`/`DECLINED`. It does not send iMIP (phase 3). If the user is the organizer, return `user:error`. Stale revision → `user:error` with message containing `stale`. Disabled → `user:error` `calendar not enabled`.

Map `calendar.ErrStale` / `ErrNotFound` / `ErrLimit` / `ErrInvalid` / `ErrNotEnabled` to `xcheckuserf` / `user:error` like existing webmail.

Auth: methods panic like the rest of webmail when `Account` is nil (sherpa wrapper requires login except Login/LoginPrep).

- [ ] **Step 1: Write the failing test**

Follow `webmail/api_test.go` to get a logged-in `Webmail` value and ctx with `requestInfo`. Then:

```go
func TestCalendarAPI(t *testing.T) {
    // setup from TestAPI; then:
    mox.Conf.Calendar.EnableCalendar = true
    acc.DB.Write(...) // ensureDefaultCalendar: reopen or call ensure
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
    mox.Conf.Calendar.EnableCalendar = false
    w := Webmail{}
    tneedError(t, func() { w.CalendarQuery(ctx, time.Now(), time.Now().Add(time.Hour)) })
}
```

If enabling calendar after `OpenAccount`, call `store` ensure by writing a Calendar row in the test or reopen account after setting the flag. Preferred: set `mox.Conf.Calendar.EnableCalendar = true` **before** `OpenAccount` in this test.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./webmail -run 'TestCalendarAPI' -count=1`

Expected: FAIL, methods missing.

- [ ] **Step 3: Write minimal implementation**

Implement methods in `webmail/calendar.go`. Run `./genapidoc.sh` and `./gents.sh webmail/api.json webmail/api.ts` so sherpa docs match (init of webmail parses api.json).

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./webmail -run 'TestCalendarAPI|TestAPI' -count=1`

Expected: PASS. Existing `TestAPI` still passes.

- [ ] **Step 5: Commit**

```bash
git add webmail/calendar.go webmail/calendar_test.go webmail/api.json webmail/api.ts
git commit -m "feat: expose calendar JSON API on webmail"
```

---

### Task 11: Docs command and regression gate

**Files:**
- Modify: `gendoc.sh` — after describing domains.conf, add a section `# calendar.conf` from `./mox config describe-calendar`, and change the intro sentence from "Mox uses two config files" to "Mox uses two required config files, and an optional third:"
- Modify: `config/doc.go` is generated; do not hand-edit; regenerate via `./gendoc.sh` after `go build` of the describe command
- Modify: none of `mox.conf` / `domains.conf` examples in a way that changes parse results

**Interfaces:**
- Consumes: `cmdConfigDescribeCalendar`
- Produces: generated docs mentioning optional `calendar.conf`

- [ ] **Step 1: Write the failing test**

In `mox-/calendar_test.go` add:

```go
func TestDescribeCalendarDoesNotAffectStatic(t *testing.T) {
    // Parse testdata/store/mox.conf still succeeds (already tested).
    // Ensure CalendarFile zero-values are optional: empty struct Describe must not panic.
    var buf bytes.Buffer
    if err := sconf.Describe(&buf, &config.CalendarFile{}); err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(buf.String(), "EnableCalendar") {
        t.Fatalf("describe: %s", buf.String())
    }
}
```

This test may already pass after Task 1. If it does, keep it as the regression lock.

Add a compile/docs check: `gendoc.sh` is not run inside `go test`. Instead run `CGO_ENABLED=0 go test ./...` as the regression gate for this task.

- [ ] **Step 2: Run test to verify it fails**

If Task 1 already implemented Describe, this test passes. Proceed to regenerate docs. If `gendoc.sh` still says "two config files", that is the remaining work — not a unit-test red. Treat the gendoc intro as the implementation step.

- [ ] **Step 3: Write minimal implementation**

Update `gendoc.sh` intro and add calendar.conf section. Run `./gendoc.sh` (requires built `./mox`). Do not change describe-static output; do not rewrite existing testdata configs.

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./mox- ./icalendar ./calendar ./store ./webmail -count=1`

Then: `CGO_ENABLED=0 go test ./imapserver ./smtpserver ./queue ./config -count=1`

Expected: PASS with no changes to existing assertions.

- [ ] **Step 5: Commit**

```bash
git add gendoc.sh config/doc.go doc.go
git commit -m "docs: document optional calendar.conf"
```

---

## Self-review

**Spec coverage (phase 1 only):**

| Spec item | Task |
|---|---|
| Optional `calendar.conf`, fail closed, no mox.conf/domains.conf changes | 1, 11 |
| Feature gate | 1 (`EnableCalendar`) |
| Canonical store, ICS not a second source of truth | 7, 8 (ICS is encoding of the row) |
| Event fields: UID, SEQUENCE, timestamps, organizer, attendees, title/description/location/status, start/end with zone, RRULE/RDATE/EXDATE, RECURRENCE-ID, unknown properties | 3–8 |
| UTC comparison, original TZ preserved, DST-stable local time | 4, 5 |
| Series or single occurrence change/cancel; no THISANDFUTURE user action | 8 |
| JSON API query/get/create/update/delete/participation-response; revision/ETag | 8, 9, 10 |
| Limits | 1, 8, 9 |
| Default calendar on activation | 7 |
| Existing tests unchanged when calendar.conf absent | 1, 7, 10, 11 |

**Out of this plan (later phases):** CalDAV, iTIP/iMIP, webmail Agenda UI, ScheduledMessage, virtual IMAP Scheduled, backup/export docs, free/busy.

**Placeholder scan:** none remaining; defaults and signatures are concrete.

**Type consistency:** `store.CalendarEvent` is the persisted row; `calendar.EventInput` is the domain write DTO; `webmail.CalendarEventView` is the JSON DTO. `icalendar.DateTime` is the shared time representation.
