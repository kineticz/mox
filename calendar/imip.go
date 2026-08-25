package calendar

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/message"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

type IncomingMeta struct {
	MailFrom         string
	MsgFrom          string
	MsgFromValidated bool
}

// ProcessIncomingMail walks a delivered message for text/calendar parts and
// applies each with ApplyIncoming. Errors are logged; the return value must be
// ignored by SMTP so delivery is never rolled back.
//
// ../rfc/6047:2.4 iMIP uses text/calendar in email
// ../rfc/5546 iTIP methods REQUEST, REPLY, CANCEL
func ProcessIncomingMail(ctx context.Context, log mlog.Log, acc *store.Account, m *store.Message, msgFile *os.File) error {
	if !mox.IMIPEnabled() {
		return nil
	}

	var r io.ReaderAt = msgFile
	if len(m.MsgPrefix) > 0 {
		r = store.FileMsgReader(m.MsgPrefix, msgFile)
	}
	p, err := message.Parse(log.Logger, false, r)
	if err != nil {
		log.Infox("parsing incoming mail for calendar", err)
		return nil
	}
	if err := p.Walk(log.Logger, nil); err != nil {
		log.Infox("walking incoming mail parts for calendar", err)
	}

	meta := IncomingMeta{
		MailFrom:         m.MailFrom,
		MsgFromValidated: m.MsgFromValidated,
	}
	if m.MsgFromLocalpart != "" || m.MsgFromDomain != "" {
		meta.MsgFrom = m.MsgFromLocalpart.String() + "@" + m.MsgFromDomain
	}

	applyIncomingParts(ctx, log, acc, &p, meta)
	return nil
}

func applyIncomingParts(ctx context.Context, log mlog.Log, acc *store.Account, p *message.Part, meta IncomingMeta) {
	if strings.EqualFold(p.MediaType, "TEXT") && strings.EqualFold(p.MediaSubType, "CALENDAR") {
		applyIncomingPart(ctx, log, acc, p, meta)
	}
	for i := range p.Parts {
		applyIncomingParts(ctx, log, acc, &p.Parts[i], meta)
	}
}

func applyIncomingPart(ctx context.Context, log mlog.Log, acc *store.Account, p *message.Part, meta IncomingMeta) {
	max := int64(Limits().MaxICSBytes)
	if p.DecodedSize > max {
		log.Info("skipping calendar part larger than MaxICSBytes", slog.Int64("size", p.DecodedSize), slog.Int64("limit", max))
		return
	}
	body, err := io.ReadAll(io.LimitReader(p.Reader(), max+1))
	if err != nil {
		log.Infox("reading calendar part", err)
		return
	}
	if int64(len(body)) > max {
		log.Info("skipping calendar part larger than MaxICSBytes", slog.Int("size", len(body)), slog.Int64("limit", max))
		return
	}
	cal, err := icalendar.Parse(body)
	if err != nil {
		log.Infox("parsing calendar part", err)
		return
	}
	result, err := ApplyIncoming(ctx, acc, cal, meta)
	if err != nil {
		log.Infox("applying incoming calendar", err)
		return
	}
	log.Debug("applied incoming calendar part", slog.String("result", result))
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
		return applyCancel(ctx, acc, vevents, uid, seq, meta, record)
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

	if _, err := putObject(ctx, acc, uid, cal.Serialize(), "", false); err != nil {
		if applyUserError(err) {
			return record("ignored-mismatch")
		}
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
	if _, err := respond(ctx, acc, master.ID, master.Revision, addr, partStat, false); err != nil {
		if applyUserError(err) {
			return record("ignored-mismatch")
		}
		return "", err
	}
	return record("applied")
}

func applyCancel(ctx context.Context, acc *store.Account, vevents []icalendar.Component, uid string, seq int, meta IncomingMeta, record func(string) (string, error)) (string, error) {
	if !envelopeMatches(componentOrganizer(vevents), meta) {
		return record("ignored-mismatch")
	}
	existing, err := storedEvents(ctx, acc, uid)
	if err != nil {
		return "", err
	}
	if len(existing) == 0 {
		return record("ignored-mismatch")
	}
	if seq < maxSequence(existing) {
		return record("ignored-stale")
	}
	err = deleteObject(ctx, acc, uid, "", false)
	if applyUserError(err) {
		return record("ignored-mismatch")
	}
	if err != nil {
		return "", err
	}
	return record("applied")
}

func applyUserError(err error) bool {
	return errors.Is(err, ErrInvalid) || errors.Is(err, ErrLimit) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStale) || errors.Is(err, ErrNotEnabled)
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
	org := normalizeCalendarAddress(organizer)
	if org == "" {
		return false
	}
	mail := normalizeCalendarAddress(meta.MailFrom)
	if mail != "" && mail == org {
		return true
	}
	msg := normalizeCalendarAddress(meta.MsgFrom)
	return msg != "" && msg == org
}

func normalizeCalendarAddress(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "mailto:")
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
