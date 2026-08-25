# Calendar Phase 3 iTIP/iMIP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add iTIP/iMIP scheduling over email behind `EnableIMIP`: transactional outbox for REQUEST/REPLY/CANCEL, incoming `text/calendar` processing that never fails mail delivery, and idempotent queue submission.

**Architecture:** Canonical events stay in `store.CalendarEvent`. Package `calendar` owns apply rules and the outbox. Package `icalendar` only parses/serializes `METHOD`. SMTP/queue call `calendar.ProcessIncomingMail` after a successful `DeliverMailbox` and ignore its error. An outbox worker (`calendar.FlushOutbox`) turns rows into MIME and `queue.Add`; retries are keyed by `IdempotencyKey`.

**Tech Stack:** Go 1.25, existing `message.Parse`, `queue.Add` / `queue.MakeMsg`, `store.Message` envelope fields (`MailFrom`, `MsgFrom*`, validation flags). No third-party iTIP libraries.

## Global Constraints

- All existing IMAP- and SMTP-functions of mox remain working.
- Existing configuration files and their format remain unchanged. New settings exist only in the optional separate file `calendar.conf`.
- Absence of `calendar.conf` means existing SMTP FUTURERELEASE and webmail behave as before; calendar service, CalDAV endpoints and virtual Scheduled mailbox are disabled.
- If `calendar.conf` is present but invalid, mox does not start; never start with partially applied new settings.
- EnableIMIP requires EnableCalendar (already in `ValidateCalendarFile`).
- Canonical calendar data stays on the mox server. The original email remains normal mail; applying iTIP must not delete or rewrite the message.
- ORGANIZER and ATTENDEE are not trusted on content alone. Envelope sender (`MailFrom` / `MsgFrom`) is compared; mismatches are not applied; the mail is kept. DMARC (`MsgFromValidated`) is evidence, not absolute authorization. If neither MailFrom nor MsgFrom matches the claimed organizer (REQUEST/CANCEL) or attendee (REPLY), skip apply.
- Only the organizer produces organizer updates (REQUEST/CANCEL). An attendee REPLY changes only that attendee’s PARTSTAT on the organizer’s event.
- Old sequences and exact duplicates are ignored. Ambiguous conflicts are not auto-merged; mail is kept.
- A logical action causes at most one scheduling update, including after retries or process restart (IdempotencyKey).
- Incoming processing must not fail SMTP/IMAP delivery. Log and return; delivery already committed.
- Version 1: methods REQUEST, REPLY, CANCEL only. No VFREEBUSY, no COUNTER, no REFRESH.
- Incoming `RANGE=THISANDFUTURE`: CANCEL sets master RRULE UNTIL to the Recurrence-ID instant (exclusive of that instance). REQUEST with THISANDFUTURE is not applied (conflict; mail kept).
- Do not add third-party iCalendar/iTIP libraries.
- `CGO_ENABLED=0 go test` via `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test …`. Do not run `./calendar` and `./store` in parallel.
- Cross-reference `// ../rfc/5546:section` and `// ../rfc/6047:section`. Do not add an RFC-fetch task.

## Locked implementation details

### Feature gate

```go
func IMIPEnabled() bool {
    return Conf.Calendar.EnableCalendar && Conf.Calendar.EnableIMIP
}
```

Update `EnableIMIP` sconf-doc: drop `Unused in phase 1.` Keep `Requires EnableCalendar. Default: false.`

When IMIP is off: no outbox rows, `ProcessIncomingMail` is a no-op, `FlushOutbox` is a no-op. Create/Update/Delete/Respond behave as phase 1/2.

### Store

Add to `store` (ICS parse stays out of store):

```go
type CalendarOutbox struct {
    ID              int64
    Method          string `bstore:"nonzero"` // REQUEST, REPLY, CANCEL
    UID             string `bstore:"nonzero"`
    Sequence        int
    Recipient       string `bstore:"nonzero"` // mailto or addr
    ICS             []byte `bstore:"nonzero"`
    IdempotencyKey  string `bstore:"nonzero,unique"`
    Created         time.Time `bstore:"default now"`
    Queued          bool
}

type CalendarInbound struct {
    ID             int64
    MessageID      int64  // store.Message.ID; 0 in unit tests
    Method         string
    UID            string
    Sequence       int
    Result         string // applied, ignored-stale, ignored-duplicate, ignored-mismatch, conflict
    Created        time.Time `bstore:"default now"`
}
```

Add to `DBTypes`. `CalendarEvent` gains optional `Organizer` already; no extra event fields required (SEQUENCE already exists).

`IdempotencyKey` format: `{method}|{uid}|{sequence}|{lowercase-recipient}`.

### Package boundaries

| Package | Must | Must not |
|---|---|---|
| `icalendar` | METHOD on VCALENDAR | store, queue, HTTP |
| `store` | outbox/inbound types | ICS parse, SMTP |
| `calendar` | ApplyIncoming, enqueue outbox, FlushOutbox, ProcessIncomingMail | IMAP, changing mail bytes |
| `smtpserver` / `queue` | call ProcessIncomingMail after successful DeliverMailbox | interpret METHOD |

### ApplyIncoming rules

Input: parsed `icalendar.Calendar`, envelope `mailFrom` and `msgFrom` strings (may be empty), `msgFromValidated bool`, account.

1. Read METHOD (uppercase). Unknown / missing → `ignored-mismatch` (no calendar change).
2. SplitSeries VEVENTs. Require ≥1 VEVENT.
3. REQUEST: claimed ORGANIZER must match mailFrom **or** msgFrom (calendarAddressMatch). If neither matches, `ignored-mismatch`. If UID absent: PutObject-equivalent create (same validation as PutObject). If UID exists: if incoming Sequence < stored Sequence → `ignored-stale`; if equal and ICS UID+Sequence already applied (inbound row with same method/uid/sequence/applied) → `ignored-duplicate`; if equal or greater: replace object (PutObject empty ifMatch). Attendee list/indexes from components.
4. REPLY: account must own the master (ORGANIZER matches an address of this account — use `acc` destinations: lock to match `ev.Organizer` against the account’s login addresses via `store` account addresses from `mox.Conf` Account.Destinations / the addresses used in tests: organizer string on the stored event must `calendarAddressMatch` the event Organizer, and the REPLY ATTENDEE with PARTSTAT must match envelope from). Update only that attendee’s PartStat via existing Respond-like logic **without** bumping Sequence. Sequence on REPLY: if incoming Sequence < stored → `ignored-stale`.
5. CANCEL: organizer match as REQUEST. If THISANDFUTURE param on RECURRENCE-ID: set master RRULE UNTIL to that instant (serialize UNTIL in UTC `YYYYMMDDTHHMMSSZ`) and do not delete earlier instances; bump Sequence. Else DeleteObject UID (unconditional ifMatch empty).
6. Always insert `CalendarInbound` with the Result. Never return an error that the SMTP caller must fail on; ApplyIncoming returns `(result string, err error)` where parse errors are `err` for the worker to log, SMTP still succeeds.

Export `calendarAddressMatch` as `AddressMatch` for tests.

### Outbox enqueue

When `IMIPEnabled()`:

- Create/PutObject/Update of a master with attendees and Organizer set → one REQUEST outbox row per attendee (not the organizer).
- Respond → one REPLY row to the Organizer address.
- Delete of a master that has attendees → CANCEL rows per attendee.
- Same transaction as the event write. FlushOutbox runs **after** the write lock is released.

FlushOutbox: list `Queued==false`, for each build a single-part MIME message (`Content-Type: text/calendar; charset=utf-8; method=REQUEST` with method matching the row), From=organizer/account, To=recipient, Subject=`Invitation: {summary}` / `Accepted: {summary}` / `Cancelled: {summary}`, `queue.MakeMsg` + `queue.Add` with senderAccount=`acc.Name`. On success set `Queued=true`. If `queue.Add` fails, leave Queued false (retry). If a row with same IdempotencyKey already Queued, skip (unique index prevents dup insert).

Sender path: parse Organizer or first destination address of the account. Tests set Organizer to `mjl@mox.example`.

### Incoming MIME

`ProcessIncomingMail(ctx, log, acc, m *store.Message, msgFile *os.File)`:

- If `!IMIPEnabled()` return nil.
- `message.Parse` the file (use `store.FileMsgReader(m.MsgPrefix, msgFile)` if MsgPrefix set, else msgFile).
- Walk `Part` tree; any `text/calendar` (case-insensitive Content-Type) with size ≤ MaxICSBytes: read body, `icalendar.Parse`, `ApplyIncoming`.
- Multiple calendar parts: apply each independently.
- Errors: log, return nil to SMTP.

Hook: after `DeliverMailbox` success in `smtpserver/server.go` (inside the delivered=true path, after unlocking is OK — call **after** WithWLock returns so IMIP can take its own lock). Copy the same one-liner into other deliver-success sites only if they are local user Inbox delivery from SMTP (`smtpserver` is required; `queue/dsn.go` and unix socket are not iMIP sources). Lock: **only `smtpserver/server.go`** after successful DeliverMailbox.

### Serve worker

In `serve` startup (same place as other goroutines): if we cannot cheaply know IMIP at start (config can be loaded), always start a 30s ticker that no-ops when `!IMIPEnabled()`. For each open account… too heavy. Lock: **no global ticker**. FlushOutbox is called after each mutating API (Create/Update/Delete/Respond/PutObject) and after ProcessIncomingMail Apply that enqueues REPLY. Tests call FlushOutbox. Document that a process crash before FlushOutbox leaves Queued=false rows; next mutation or `mox` restart should flush.

Restart: in `serve` after store init, if `IMIPEnabled()`, iterate accounts… expensive. Simpler: `calendar.FlushAllAccounts` on serve start that lists configured account names from `mox.Conf.Static.Accounts` (or Dynamic), OpenAccount, FlushOutbox, Close. One function `FlushAllOutboxes(ctx, log)` called from serve.go once at start and not on a ticker.

Find serve.go account list via existing patterns (`mox.Conf.Accounts()` or similar). If no iterator exists, skip restart flush and only Flush after mutations + document crash window (YAGNI). **Lock: skip restart scan.** Crash window until next calendar write is accepted. Mention in concerns.

### Out of this plan

CalDAV scheduling (RFC 6638), webmail Agenda UI, ScheduledMessage, COUNTER/REFRESH, sending HTML invitation bodies, IMIP on DSN/unix delivery.

---

### Task 1: IMIPEnabled and VCALENDAR METHOD

**Files:**
- Modify: `mox-/calendar.go`, `mox-/calendar_test.go`
- Modify: `config/calendar.go` sconf-doc
- Modify: `icalendar/object.go` — `func (c Calendar) Method() string`
- Modify: `icalendar/object_test.go`
- Test: those packages

**Interfaces:**
- Produces: `mox.IMIPEnabled() bool`; `Calendar.Method() string` (first METHOD property, uppercase, `""` if absent)

- [ ] **Step 1: Write the failing test**

```go
func TestIMIPEnabledRequiresBothFlags(t *testing.T) {
    orig := Conf.Calendar
    t.Cleanup(func() { Conf.Calendar = orig })
    Conf.Calendar = config.CalendarFile{}
    if IMIPEnabled() {
        t.Fatal("disabled")
    }
    Conf.Calendar.EnableIMIP = true
    if IMIPEnabled() {
        t.Fatal("imip without calendar")
    }
    Conf.Calendar.EnableCalendar = true
    if !IMIPEnabled() {
        t.Fatal("both true")
    }
}
```

```go
func TestCalendarMethod(t *testing.T) {
    cal, err := Parse([]byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:x@mox\r\nDTSTART:20260815T090000Z\r\nDTEND:20260815T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"))
    if err != nil || cal.Method() != "REQUEST" {
        t.Fatalf("%v %#v", err, cal)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./mox- ./icalendar -count=1 -run 'TestIMIPEnabledRequiresBothFlags|TestCalendarMethod'`

Expected: FAIL (IMIPEnabled / Method undefined).

- [ ] **Step 3: Write minimal implementation**

`IMIPEnabled` as locked. `Method()` reads Prop METHOD on Calendar (add `func (c Calendar) Prop` mirroring Component.Prop if missing).

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./mox- ./icalendar ./config -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add mox-/calendar.go mox-/calendar_test.go config/calendar.go icalendar/object.go icalendar/object_test.go
git commit -m "feat: gate iMIP and parse VCALENDAR METHOD"
```

---

### Task 2: Outbox and inbound store types

**Files:**
- Modify: `store/calendar.go` — CalendarOutbox, CalendarInbound
- Modify: `store/account.go` — DBTypes
- Modify: `store/calendar_test.go` or `calendar/service_test.go` — insert/get unique IdempotencyKey
- Test: `store` or `calendar` using existing testAccount

**Interfaces:**
- Produces: types as locked; unique IdempotencyKey

- [ ] **Step 1: Write the failing test**

In `calendar/service_test.go` (store types available):

```go
func TestCalendarOutboxUniqueIdempotencyKey(t *testing.T) {
    acc := testAccount(t)
    row := store.CalendarOutbox{Method: "REQUEST", UID: "u@mox", Recipient: "a@b", ICS: []byte("BEGIN:VEVENT\r\nEND:VEVENT\r\n"), IdempotencyKey: "REQUEST|u@mox|0|a@b"}
    if err := acc.DB.Write(testctx, func(tx *bstore.Tx) error {
        return tx.Insert(&row)
    }); err != nil {
        t.Fatal(err)
    }
    dup := row
    dup.ID = 0
    err := acc.DB.Write(testctx, func(tx *bstore.Tx) error {
        return tx.Insert(&dup)
    })
    if err == nil {
        t.Fatal("expected unique error")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./calendar -count=1 -run TestCalendarOutboxUniqueIdempotencyKey`

Expected: FAIL (type unknown).

- [ ] **Step 3: Write minimal implementation**

Add types + DBTypes entries.

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./calendar ./store -count=1` — run store **before** calendar or as two commands.

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add store/calendar.go store/account.go calendar/service_test.go
git commit -m "feat: add calendar iMIP outbox and inbound records"
```

---

### Task 3: ApplyIncoming REQUEST/REPLY/CANCEL

**Files:**
- Create: `calendar/imip.go`
- Modify: `calendar/service.go` — export `AddressMatch`
- Create: `calendar/imip_test.go`
- Test: `calendar/imip_test.go`

**Interfaces:**
- Consumes: PutObject, GetObject, DeleteObject, Respond, AddressMatch, IMIPEnabled
- Produces:

```go
type IncomingMeta struct {
    MailFrom         string
    MsgFrom          string
    MsgFromValidated bool
}

func ApplyIncoming(ctx context.Context, acc *store.Account, cal icalendar.Calendar, meta IncomingMeta) (result string, err error)
```

Results: `applied`, `ignored-stale`, `ignored-duplicate`, `ignored-mismatch`, `conflict`. If `!IMIPEnabled()` return `"ignored-mismatch", nil` without writes.

- [ ] **Step 1: Write the failing test**

Enable IMIP on testAccount config. REQUEST from organizer `org@mox.example` matching MailFrom creates event. Second identical REQUEST → `ignored-duplicate`. Lower SEQUENCE → `ignored-stale`. REQUEST with MailFrom not matching ORGANIZER → `ignored-mismatch`. REPLY updates PARTSTAT only. CANCEL deletes. THISANDFUTURE REQUEST → `conflict`.

Use PutObject to seed organizer event for REPLY tests.

- [ ] **Step 2: Run test to verify it fails**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./calendar -count=1 -run ApplyIncoming`

Expected: FAIL (ApplyIncoming undefined).

- [ ] **Step 3: Write minimal implementation**

As locked rules. Insert CalendarInbound on every path. Duplicate detection: QueryTx CalendarInbound FilterEqual Method, UID, Sequence, Result=`applied`.

- [ ] **Step 4: Run test to verify it passes**

Run: `podman run --rm -e CGO_ENABLED=0 -v "$PWD:/src:Z" -w /src docker.io/library/golang:1.25 go test ./calendar -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add calendar/imip.go calendar/imip_test.go calendar/service.go
git commit -m "feat: apply incoming iTIP REQUEST REPLY CANCEL"
```

---

### Task 4: Enqueue outbox on local mutations

**Files:**
- Modify: `calendar/imip.go` or `calendar/outbox.go` — `enqueue`, `FlushOutbox`
- Modify: `calendar/service.go` — Create/Update/Delete/Respond/PutObject call enqueue when IMIPEnabled
- Modify: `calendar/imip_test.go`
- Test: calendar

**Interfaces:**
- Produces: `func FlushOutbox(ctx context.Context, log mlog.Log, acc *store.Account) error`
- Enqueue inside the existing DB.Write (same tx). FlushOutbox after WithWLock returns (callers of Service methods). Service methods themselves call FlushOutbox after the lock if IMIPEnabled (so HTTP/JSON/CalDAV all flush). FlushOutbox must not hold the account write lock while calling queue.Add if that can re-enter; Open queue DB only. If `queue.Add` is heavy in unit tests, FlushOutbox in tests can run with queue not inited — then skip Add and leave Queued false OR init queue in tests.

**Test lock:** Do **not** require live `queue.Add` in this task. Enqueue-only tests: after Create with attendee, a CalendarOutbox row exists with Queued=false. FlushOutbox without queue init returns error or no-ops if queue DB missing. Prefer: FlushOutbox is Task 5. **This task only inserts outbox rows.** Do not call queue.

Create with attendees+Organizer inserts REQUEST rows. Respond inserts REPLY. Delete master with attendees inserts CANCEL. Unique IdempotencyKey: second identical Create path must not happen (UID unique). Update that changes summary increments Sequence → new REQUEST rows with new Sequence in the key.

- [ ] **Step 1: Write the failing test**

Create event with Organizer and one Attendee with IMIP on; expect one CalendarOutbox REQUEST. Respond as that attendee; expect REPLY row. Delete; expect CANCEL row.

- [ ] **Step 2: Run test to verify it fails**

Expected: no outbox rows.

- [ ] **Step 3: Write minimal implementation**

- [ ] **Step 4: Run tests**

`go test ./calendar -count=1` PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat: enqueue iMIP REQUEST REPLY CANCEL in outbox"
```

---

### Task 5: FlushOutbox to queue.Add

**Files:**
- Modify: `calendar/outbox.go` — FlushOutbox builds MIME and queue.Add
- Create: `calendar/outbox_test.go` — use testdata like queue tests if possible; if queue.Init is too heavy, test MIME builder separately and FlushOutbox with a fake by extracting `buildIMIPMessage(row) (msgFile *os.File, qm queue.Msg, err error)`
- Test: calendar

**Interfaces:**
- Produces: `func buildIMIPMessage(fromAddr, toAddr, subject string, method string, ics []byte) ([]byte, error)` full RFC 5322 message
- `FlushOutbox` uses `queue.MakeMsg` + `queue.Add`

Look at `queue` test setup (`queue/main_test.go`) and copy the minimal Init. If that pulls too much, unit-test `buildIMIPMessage` only and call `queue.Add` in an integration test that skips when queue cannot init.

**Lock:** `buildIMIPMessage` is unit-tested (Content-Type contains `method=REQUEST`, body contains BEGIN:VCALENDAR). `FlushOutbox` integration: if `queue` tests already have Init helper, use it; otherwise FlushOutbox marks Queued=true only after successful Add; test with queue Init from `../testdata/queue` or skip integration with `t.Skip` **forbidden**. Must have one successful Add test.

Inspect `queue/main_test.go` for setup; copy that pattern into `calendar/outbox_test.go`.

- [ ] **Step 1–5:** TDD buildIMIPMessage then FlushOutbox. Commit:

```bash
git commit -m "feat: submit iMIP outbox messages to the mail queue"
```

---

### Task 6: ProcessIncomingMail and SMTP hook

**Files:**
- Modify: `calendar/imip.go` — ProcessIncomingMail
- Modify: `smtpserver/server.go` — after successful DeliverMailbox
- Create: `calendar/incoming_test.go` — parse a tiny MIME message with text/calendar REQUEST
- Modify: `smtpserver` test only if there is a cheap deliver-to-inbox test; otherwise calendar-level ProcessIncomingMail is enough plus a smtpserver comment. **Lock:** add `ProcessIncomingMail` unit test with a raw MIME file in `testdata` or inline string. SMTP hook: 5–10 lines after deliver success. Add one smtpserver test only if existing deliver tests can EnableIMIP without huge setup; if not, hook without new smtpserver test (controller verifies hook exists at task review).

**Interfaces:**
- Produces: `func ProcessIncomingMail(ctx context.Context, log mlog.Log, acc *store.Account, m *store.Message, msgFile *os.File) error` always returns nil after logging (or returns err that SMTP **must ignore**). SMTP: `go` not required; call synchronously after lock release; errors logged.

Walk parts recursively (`Part.Parts`). Content-Type `text/calendar`.

- [ ] **Step 1–5:** TDD ProcessIncomingMail. Commit:

```bash
git commit -m "feat: process incoming calendar MIME after delivery"
```

---

### Task 7: Docs and THISANDFUTURE CANCEL

**Files:**
- Modify: `calendar/imip.go` — THISANDFUTURE CANCEL UNTIL
- Modify: `calendar/imip_test.go`
- Modify: `gendoc.sh` / `config/doc.go` via gendoc if EnableIMIP comment changed
- Test: calendar + `go test ./config ./mox-`

If Task 3 already implemented THISANDFUTURE CANCEL, this task is docs-only plus a regression test.

- [ ] **Step 1:** Test CANCEL with RECURRENCE-ID and RANGE=THISANDFUTURE leaves earlier instance queryable and omits that instant and later (Query window).
- [ ] **Step 3:** Implement UNTIL on master RRULE.
- [ ] **Step 5:** Commit `feat: honor THISANDFUTURE on incoming iTIP CANCEL` and/or `docs: document EnableIMIP`

---

## Self-review

**Spec coverage (phase 3):**

| Spec item | Task |
|---|---|
| EnableIMIP gate | 1 |
| Outgoing invitation event+outbox, worker → queue | 4, 5 |
| Incoming MIME, mail retained, validate organizer/attendee/METHOD/UID/SEQUENCE | 3, 6 |
| Reply changes only that attendee | 3 |
| Stale/duplicate/mismatch not blindly applied | 3 |
| Idempotent retries | 2 unique key, 5 Queued flag |
| THISANDFUTURE incoming | 3/7 CANCEL only; REQUEST conflict |
| Delivery never fails because of iMIP | 6 |

**Placeholder scan:** queue test setup must be copied from `queue/main_test.go` in Task 5 (read that file; do not invent Init). SMTP hook site is `smtpserver/server.go` after DeliverMailbox success.

**Type consistency:** CalendarOutbox / CalendarInbound / ApplyIncoming result strings as locked.
