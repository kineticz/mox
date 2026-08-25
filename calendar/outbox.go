package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

var pkglog = mlog.New("calendar", nil)

// FlushOutbox will send unqueued CalendarOutbox rows. This stub returns nil
// and does not touch the mail queue.
func FlushOutbox(ctx context.Context, log mlog.Log, acc *store.Account) error {
	return nil
}

func flushAfterMutation(ctx context.Context, acc *store.Account, err error) {
	if err != nil || !mox.IMIPEnabled() {
		return
	}
	_ = FlushOutbox(ctx, pkglog, acc)
}

// ../rfc/5546:3.2.2 REQUEST
// ../rfc/5546:3.2.3 REPLY
// ../rfc/5546:3.2.5 CANCEL
func enqueueOutbox(tx *bstore.Tx, method string, ev store.CalendarEvent) error {
	if !mox.IMIPEnabled() {
		return nil
	}
	if ev.RecurrenceID != "" {
		return nil
	}
	switch method {
	case "REQUEST":
		if ev.Organizer == "" || len(ev.Attendees) == 0 {
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
	case "CANCEL":
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
	default:
		return fmt.Errorf("unknown outbox method %q", method)
	}
	return nil
}

func enqueueCancelForUID(tx *bstore.Tx, ev store.CalendarEvent) error {
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
	return enqueueOutbox(tx, "CANCEL", master)
}

func insertOutboxRow(tx *bstore.Tx, method string, ev store.CalendarEvent, recipient string) error {
	recip := strings.ToLower(recipient)
	if recip == "" {
		return nil
	}
	key := fmt.Sprintf("%s|%s|%d|%s", method, ev.UID, ev.Sequence, recip)
	ics := methodICS(method, ev.ICS)
	row, err := bstore.QueryTx[store.CalendarOutbox](tx).FilterEqual("IdempotencyKey", key).Get()
	if errors.Is(err, bstore.ErrAbsent) {
		return tx.Insert(&store.CalendarOutbox{
			Method:         method,
			UID:            ev.UID,
			Sequence:       ev.Sequence,
			Recipient:      recip,
			ICS:            ics,
			IdempotencyKey: key,
			Queued:         false,
		})
	}
	if err != nil {
		return err
	}
	if row.Queued || method != "REPLY" {
		return nil
	}
	row.ICS = ics
	return tx.Update(&row)
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
