package calendar

import (
	"context"
	"errors"
	"strings"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

type IncomingMeta struct {
	MailFrom         string
	MsgFrom          string
	MsgFromValidated bool
}

func ApplyIncoming(ctx context.Context, acc *store.Account, cal icalendar.Calendar, meta IncomingMeta) (result string, err error) {
	if !mox.IMIPEnabled() {
		return "ignored-mismatch", nil
	}

	method := cal.Method()
	vevents := calendarVEVENTs(cal)
	uid, seq := uidSequence(vevents)

	record := func(result string) (string, error) {
		return result, insertInbound(ctx, acc, method, uid, seq, result)
	}

	switch method {
	case "REQUEST", "REPLY", "CANCEL":
	default:
		return record("ignored-mismatch")
	}
	if len(vevents) == 0 {
		return record("ignored-mismatch")
	}
	if _, _, err := icalendar.SplitSeries(vevents); err != nil {
		return "", err
	}

	switch method {
	case "REQUEST":
		return applyRequest(ctx, acc, cal, vevents, uid, seq, meta, record)
	case "REPLY":
		return applyReply(ctx, acc, vevents, uid, seq, meta, record)
	default:
		return applyCancel(ctx, acc, vevents, uid, meta, record)
	}
}

func applyRequest(ctx context.Context, acc *store.Account, cal icalendar.Calendar, vevents []icalendar.Component, uid string, seq int, meta IncomingMeta, record func(string) (string, error)) (string, error) {
	if !envelopeMatches(componentOrganizer(vevents), meta) {
		return record("ignored-mismatch")
	}
	if hasThisAndFuture(vevents) {
		return record("conflict")
	}

	existing, err := storedEvents(ctx, acc, uid)
	if err != nil {
		return "", err
	}
	if len(existing) > 0 {
		storedSeq := maxSequence(existing)
		if seq < storedSeq {
			return record("ignored-stale")
		}
		if seq == storedSeq {
			dup, err := inboundApplied(ctx, acc, "REQUEST", uid, seq)
			if err != nil {
				return "", err
			}
			if dup {
				return record("ignored-duplicate")
			}
		}
	}

	if _, err := (Service{}).PutObject(ctx, acc, uid, cal.Serialize(), ""); err != nil {
		return "", err
	}
	return record("applied")
}

func applyReply(ctx context.Context, acc *store.Account, vevents []icalendar.Component, uid string, seq int, meta IncomingMeta, record func(string) (string, error)) (string, error) {
	existing, err := storedEvents(ctx, acc, uid)
	if err != nil {
		return "", err
	}
	var master *store.CalendarEvent
	for i := range existing {
		if existing[i].RecurrenceID == "" {
			master = &existing[i]
			break
		}
	}
	if master == nil || !accountOwnsOrganizer(acc, master.Organizer) {
		return record("ignored-mismatch")
	}
	if org := componentOrganizer(vevents); org != "" && !AddressMatch(org, master.Organizer) {
		return record("ignored-mismatch")
	}
	addr, partStat, ok := matchingReplyAttendee(vevents, meta)
	if !ok {
		return record("ignored-mismatch")
	}
	if seq < master.Sequence {
		return record("ignored-stale")
	}
	if _, err := (Service{}).Respond(ctx, acc, master.ID, master.Revision, addr, partStat); err != nil {
		return "", err
	}
	return record("applied")
}

func applyCancel(ctx context.Context, acc *store.Account, vevents []icalendar.Component, uid string, meta IncomingMeta, record func(string) (string, error)) (string, error) {
	if !envelopeMatches(componentOrganizer(vevents), meta) {
		return record("ignored-mismatch")
	}
	err := (Service{}).DeleteObject(ctx, acc, uid, "")
	if errors.Is(err, ErrNotFound) {
		return record("ignored-mismatch")
	}
	if err != nil {
		return "", err
	}
	return record("applied")
}

func insertInbound(ctx context.Context, acc *store.Account, method, uid string, seq int, result string) error {
	return acc.DB.Write(ctx, func(tx *bstore.Tx) error {
		return tx.Insert(&store.CalendarInbound{
			Method:   method,
			UID:      uid,
			Sequence: seq,
			Result:   result,
		})
	})
}

func inboundApplied(ctx context.Context, acc *store.Account, method, uid string, seq int) (bool, error) {
	var exists bool
	err := acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		exists, err = bstore.QueryTx[store.CalendarInbound](tx).
			FilterEqual("Method", method).
			FilterEqual("UID", uid).
			FilterEqual("Sequence", seq).
			FilterEqual("Result", "applied").
			Exists()
		return err
	})
	return exists, err
}

func storedEvents(ctx context.Context, acc *store.Account, uid string) ([]store.CalendarEvent, error) {
	if uid == "" {
		return nil, nil
	}
	var events []store.CalendarEvent
	err := acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		events, err = eventsByUID(tx, uid)
		return err
	})
	return events, err
}

func calendarVEVENTs(cal icalendar.Calendar) []icalendar.Component {
	var vevents []icalendar.Component
	for _, c := range cal.Components {
		if c.Name == "VEVENT" {
			vevents = append(vevents, c)
		}
	}
	return vevents
}

func uidSequence(vevents []icalendar.Component) (string, int) {
	for _, c := range vevents {
		if _, ok := c.Prop("RECURRENCE-ID"); !ok {
			return c.UID(), c.Sequence()
		}
	}
	if len(vevents) == 0 {
		return "", 0
	}
	return vevents[0].UID(), vevents[0].Sequence()
}

func componentOrganizer(vevents []icalendar.Component) string {
	for _, c := range vevents {
		if p, ok := c.Prop("ORGANIZER"); ok {
			return p.Value
		}
	}
	return ""
}

func envelopeMatches(organizer string, meta IncomingMeta) bool {
	return AddressMatch(organizer, meta.MailFrom) || AddressMatch(organizer, meta.MsgFrom)
}

func hasThisAndFuture(vevents []icalendar.Component) bool {
	for _, c := range vevents {
		p, ok := c.Prop("RECURRENCE-ID")
		if !ok {
			continue
		}
		if v, ok := p.Param("RANGE"); ok && strings.EqualFold(v, "THISANDFUTURE") {
			return true
		}
	}
	return false
}

func maxSequence(events []store.CalendarEvent) int {
	var seq int
	for _, ev := range events {
		if ev.Sequence > seq {
			seq = ev.Sequence
		}
	}
	return seq
}

func accountOwnsOrganizer(acc *store.Account, organizer string) bool {
	conf, ok := acc.Conf()
	if !ok {
		return false
	}
	for addr := range conf.Destinations {
		if AddressMatch(addr, organizer) {
			return true
		}
	}
	return false
}

func matchingReplyAttendee(vevents []icalendar.Component, meta IncomingMeta) (addr, partStat string, ok bool) {
	for _, c := range vevents {
		for _, p := range c.Props("ATTENDEE") {
			if !envelopeMatches(p.Value, meta) {
				continue
			}
			ps, has := p.Param("PARTSTAT")
			if !has || ps == "" {
				continue
			}
			return p.Value, strings.ToUpper(ps), true
		}
	}
	return "", "", false
}
