package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/config"
	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/message"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/queue"
	"github.com/mjl-/mox/smtp"
	"github.com/mjl-/mox/store"
)

var pkglog = mlog.New("calendar", nil)

// ../rfc/6047:2.4 iMIP text/calendar with method parameter
func buildIMIPMessage(fromAddr, toAddr, subject string, method string, ics []byte) (msg []byte, err error) {
	fromAddr = normalizeCalendarAddress(fromAddr)
	toAddr = normalizeCalendarAddress(toAddr)
	if fromAddr == "" || toAddr == "" {
		return nil, fmt.Errorf("missing from or to address")
	}
	from, err := smtp.ParseAddress(fromAddr)
	if err != nil {
		return nil, fmt.Errorf("parsing from address %q: %w", fromAddr, err)
	}
	to, err := smtp.ParseAddress(toAddr)
	if err != nil {
		return nil, fmt.Errorf("parsing to address %q: %w", toAddr, err)
	}
	subject = sanitizeHeaderText(subject)

	smtputf8 := from.Localpart.IsInternational() || to.Localpart.IsInternational()
	var buf bytes.Buffer
	xc := message.NewComposer(&buf, 0, smtputf8)
	defer func() {
		x := recover()
		if x == nil {
			return
		}
		if e, ok := x.(error); ok && errors.Is(e, message.ErrCompose) {
			err = e
			return
		}
		panic(x)
	}()

	xc.Header("Date", time.Now().Format(message.RFC5322Z))
	xc.HeaderAddrs("From", []message.NameAddress{{Address: from}})
	xc.HeaderAddrs("To", []message.NameAddress{{Address: to}})
	xc.Subject(subject)
	xc.Header("Message-ID", fmt.Sprintf("<%s>", mox.MessageIDGen(smtputf8)))
	xc.Header("MIME-Version", "1.0")
	cte := "7bit"
	if hasHighBit(ics) || xc.Has8bit {
		cte = "8bit"
	}
	xc.Header("Content-Type", fmt.Sprintf("text/calendar; charset=utf-8; method=%s", method))
	xc.Header("Content-Transfer-Encoding", cte)
	xc.Line()
	xc.Write(ics)
	xc.Flush()
	return buf.Bytes(), nil
}

func sanitizeHeaderText(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func hasHighBit(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

func parseSMTPPath(addr string) (smtp.Path, error) {
	a, err := smtp.ParseAddress(normalizeCalendarAddress(addr))
	if err != nil {
		return smtp.Path{}, err
	}
	return a.Path(), nil
}

func outboxSummaryOrganizer(ics []byte) (summary, organizer string) {
	cal, err := icalendar.Parse(ics)
	if err != nil {
		return "", ""
	}
	for _, c := range cal.Components {
		if c.Name != "VEVENT" {
			continue
		}
		if p, ok := c.Prop("SUMMARY"); ok && summary == "" {
			summary = p.Value
		}
		if p, ok := c.Prop("ORGANIZER"); ok && organizer == "" {
			organizer = p.Value
		}
	}
	return summary, organizer
}

// sendableDestinations returns the account addresses we may use as a From or
// match an ORGANIZER against. A catchall destination ("@domain") is not an
// address, and sorts before the real ones.
func sendableDestinations(dests map[string]config.Destination) []string {
	addrs := make([]string, 0, len(dests))
	for addr := range dests {
		if strings.HasPrefix(addr, "@") {
			continue
		}
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}

func firstDestination(acc *store.Account) string {
	conf, ok := acc.Conf()
	if !ok {
		return ""
	}
	addrs := sendableDestinations(conf.Destinations)
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

func outboxSubject(method, summary string) string {
	summary = sanitizeHeaderText(summary)
	switch method {
	case "REQUEST":
		return "Invitation: " + summary
	case "REPLY":
		return "Accepted: " + summary
	case "CANCEL":
		return "Cancelled: " + summary
	default:
		return summary
	}
}

func outboxFromAddress(acc *store.Account, method string, ics []byte) string {
	if method == "REPLY" {
		if addr := respondingAccountAttendee(acc, ics); addr != "" {
			return addr
		}
		return firstDestination(acc)
	}
	_, organizer := outboxSummaryOrganizer(ics)
	if accountOwnsOrganizer(acc, organizer) {
		return normalizeCalendarAddress(organizer)
	}
	return firstDestination(acc)
}

func respondingAccountAttendee(acc *store.Account, ics []byte) string {
	cal, err := icalendar.Parse(ics)
	if err != nil {
		return ""
	}
	var fallback string
	for _, c := range cal.Components {
		if c.Name != "VEVENT" {
			continue
		}
		for _, p := range c.Props("ATTENDEE") {
			if !accountOwnsOrganizer(acc, p.Value) {
				continue
			}
			addr := normalizeCalendarAddress(p.Value)
			ps, _ := p.Param("PARTSTAT")
			switch strings.ToUpper(ps) {
			case "ACCEPTED", "TENTATIVE", "DECLINED":
				return addr
			}
			if fallback == "" {
				fallback = addr
			}
		}
	}
	return fallback
}

// FlushOutbox submits unqueued CalendarOutbox rows to the mail queue.
func FlushOutbox(ctx context.Context, log mlog.Log, acc *store.Account) error {
	if !mox.IMIPEnabled() {
		return nil
	}
	if queue.DB == nil {
		return fmt.Errorf("queue not initialized")
	}

	var rows []store.CalendarOutbox
	err := acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		rows, err = bstore.QueryTx[store.CalendarOutbox](tx).FilterEqual("Queued", false).List()
		return err
	})
	if err != nil {
		return err
	}

	var first error
	for _, row := range rows {
		var claimed bool
		err := acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			var err error
			claimed, err = claimOutboxRow(tx, row.ID)
			return err
		})
		if err != nil {
			log.Errorx("claiming calendar outbox row", err, slog.String("uid", row.UID), slog.String("method", row.Method), slog.String("recipient", row.Recipient))
			if first == nil {
				first = err
			}
			continue
		}
		if !claimed {
			continue
		}
		if err := flushOutboxRow(ctx, log, acc, row); err != nil {
			log.Errorx("adding iMIP message to queue", err, slog.String("uid", row.UID), slog.String("method", row.Method), slog.String("recipient", row.Recipient))
			if uerr := unclaimOutboxRow(ctx, acc, row.ID); uerr != nil {
				log.Errorx("clearing calendar outbox claim", uerr, slog.Int64("id", row.ID))
			}
			if first == nil {
				first = err
			}
		}
	}
	return first
}

func claimOutboxRow(tx *bstore.Tx, id int64) (bool, error) {
	row := store.CalendarOutbox{ID: id}
	if err := tx.Get(&row); err != nil {
		return false, err
	}
	if row.Queued {
		return false, nil
	}
	row.Queued = true
	if err := tx.Update(&row); err != nil {
		return false, err
	}
	return true, nil
}

func unclaimOutboxRow(ctx context.Context, acc *store.Account, id int64) error {
	return acc.DB.Write(ctx, func(tx *bstore.Tx) error {
		row := store.CalendarOutbox{ID: id}
		if err := tx.Get(&row); err != nil {
			return err
		}
		row.Queued = false
		return tx.Update(&row)
	})
}

func flushOutboxRow(ctx context.Context, log mlog.Log, acc *store.Account, row store.CalendarOutbox) error {
	summary, _ := outboxSummaryOrganizer(row.ICS)
	fromAddr := outboxFromAddress(acc, row.Method, row.ICS)
	toAddr := normalizeCalendarAddress(row.Recipient)
	subject := outboxSubject(row.Method, summary)

	msg, err := buildIMIPMessage(fromAddr, toAddr, subject, row.Method, row.ICS)
	if err != nil {
		return err
	}
	fromPath, err := parseSMTPPath(fromAddr)
	if err != nil {
		return fmt.Errorf("parsing from address %q: %w", fromAddr, err)
	}
	toPath, err := parseSMTPPath(toAddr)
	if err != nil {
		return fmt.Errorf("parsing recipient %q: %w", toAddr, err)
	}

	parsed, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		return fmt.Errorf("parsing iMIP message: %w", err)
	}
	messageID := parsed.Header.Get("Message-ID")

	msgFile, err := store.CreateMessageTemp(log, "imip-outbox")
	if err != nil {
		return err
	}
	defer store.CloseRemoveTempFile(log, msgFile, "imip outbox message")
	if _, err := msgFile.Write(msg); err != nil {
		return err
	}

	smtputf8 := fromPath.Localpart.IsInternational() || toPath.Localpart.IsInternational()
	qm := queue.MakeMsg(fromPath, toPath, hasHighBit(msg), smtputf8, int64(len(msg)), messageID, nil, nil, time.Now(), subject)
	return queue.Add(ctx, log, acc.Name, msgFile, qm)
}

func flushAfterMutation(ctx context.Context, acc *store.Account, err error) {
	if err != nil {
		return
	}
	if ferr := FlushOutbox(ctx, pkglog, acc); ferr != nil {
		pkglog.Errorx("flushing calendar outbox", ferr)
	}
}

// ../rfc/5546:3.2.2 REQUEST
// ../rfc/5546:3.2.3 REPLY
// ../rfc/5546:3.2.5 CANCEL
func enqueueOutbox(tx *bstore.Tx, acc *store.Account, method string, ev store.CalendarEvent) error {
	if !mox.IMIPEnabled() {
		return nil
	}
	if ev.RecurrenceID != "" {
		return nil
	}
	switch method {
	case "REQUEST", "CANCEL":
		// ../rfc/5546:1.4 only the Organizer sends REQUEST and CANCEL. A local
		// write of an event organized elsewhere, e.g. a CalDAV PUT of a received
		// invitation, must stay silent.
		if !accountOwnsOrganizer(acc, ev.Organizer) {
			return nil
		}
		if len(ev.Attendees) == 0 {
			return nil
		}
		for _, att := range ev.Attendees {
			if AddressMatch(att.Address, ev.Organizer) {
				continue
			}
			if err := insertOutboxRow(tx, method, ev, att.Address); err != nil {
				return err
			}
		}
	case "REPLY":
		if ev.Organizer == "" {
			return nil
		}
		return insertOutboxRow(tx, method, ev, ev.Organizer)
	default:
		return fmt.Errorf("unknown outbox method %q", method)
	}
	return nil
}

func enqueueCancelForUID(tx *bstore.Tx, acc *store.Account, ev store.CalendarEvent) error {
	master := ev
	if ev.RecurrenceID != "" {
		series, err := eventsByUID(tx, ev.UID)
		if err != nil {
			return err
		}
		var found bool
		for i := range series {
			if series[i].RecurrenceID == "" {
				master = series[i]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return enqueueOutbox(tx, acc, "CANCEL", master)
}

// sendableRecipient normalizes a calendar address to a mailbox address we can
// actually submit to, or "" for values such as `urn:uuid:...` that never can be.
func sendableRecipient(recipient string) string {
	addr := normalizeCalendarAddress(recipient)
	if addr == "" {
		return ""
	}
	if _, err := smtp.ParseAddress(addr); err != nil {
		return ""
	}
	return addr
}

func insertOutboxRow(tx *bstore.Tx, method string, ev store.CalendarEvent, recipient string) error {
	recip := sendableRecipient(recipient)
	if recip == "" {
		return nil
	}
	// Respond does not bump SEQUENCE, so a REPLY is discriminated on the
	// revision instead: every PARTSTAT change must reach the organizer.
	discriminator := ev.Sequence
	if method == "REPLY" {
		discriminator = int(ev.Revision)
	}
	key := fmt.Sprintf("%s|%s|%d|%s", method, ev.UID, discriminator, recip)
	exists, err := bstore.QueryTx[store.CalendarOutbox](tx).FilterEqual("IdempotencyKey", key).Exists()
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return tx.Insert(&store.CalendarOutbox{
		Method:         method,
		UID:            ev.UID,
		Sequence:       ev.Sequence,
		Recipient:      recip,
		ICS:            methodICS(method, ev.ICS),
		IdempotencyKey: key,
		Queued:         false,
	})
}

// ../rfc/5545:3.7.3 PRODID is required
// ../rfc/5546:3.1 METHOD on VCALENDAR
func methodICS(method string, vevent []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//caldav//EN\r\nMETHOD:")
	buf.WriteString(method)
	buf.WriteString("\r\n")
	buf.Write(vevent)
	buf.WriteString("END:VCALENDAR\r\n")
	return buf.Bytes()
}
