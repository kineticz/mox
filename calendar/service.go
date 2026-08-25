package calendar

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

var (
	ErrNotEnabled = errors.New("calendar not enabled")
	ErrNotFound   = errors.New("event not found")
	ErrStale      = errors.New("stale revision")
	ErrLimit      = errors.New("calendar limit exceeded")
	ErrInvalid    = errors.New("invalid event")
)

type EventInput struct {
	UID          string
	RecurrenceID string
	Sequence     int
	Revision     int64
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
	Unknown      []icalendar.Property
}

type CalendarObject struct {
	UID  string
	ETag string // `"rev-{maxRevision}"` including quotes
	ICS  []byte // full VCALENDAR
}

type Service struct{}

func (Service) Create(ctx context.Context, acc *store.Account, in EventInput) (ev store.CalendarEvent, rerr error) {
	if !mox.CalendarEnabled() {
		return ev, ErrNotEnabled
	}
	if in.UID == "" {
		uid, err := newUID()
		if err != nil {
			return ev, err
		}
		in.UID = uid
	}
	if err := validateInput(in); err != nil {
		return ev, err
	}

	acc.WithWLock(func() {
		rerr = acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			calendar, err := acc.DefaultCalendar(tx)
			if err != nil {
				return err
			}
			series, err := eventsByUID(tx, in.UID)
			if err != nil {
				return err
			}
			var master *store.CalendarEvent
			for i := range series {
				if series[i].RecurrenceID == "" {
					master = &series[i]
					break
				}
			}
			if in.RecurrenceID != "" {
				if master == nil {
					return fmt.Errorf("%w: exception requires an existing master", ErrInvalid)
				}
			} else if master != nil {
				return fmt.Errorf("%w: master already exists", ErrInvalid)
			}
			existingExceptions, err := storedExceptionCount(series, 0)
			if err != nil {
				return err
			}
			inputExceptions, err := inputExceptionCount(in)
			if err != nil {
				return err
			}
			if existingExceptions+inputExceptions > Limits().MaxExceptions {
				return fmt.Errorf("%w: too many exceptions", ErrLimit)
			}

			now := time.Now().UTC()
			ev = eventFromInput(in)
			ev.CalendarID = calendar.ID
			ev.Revision = 1
			ev.Created = now
			ev.LastModified = now
			ics, err := serializeEvent(ev, in.Unknown)
			if err != nil {
				return err
			}
			ev.ICS = ics
			if len(ev.ICS) > Limits().MaxICSBytes {
				return fmt.Errorf("%w: ICS is too large", ErrLimit)
			}
			if err := tx.Insert(&ev); err != nil {
				return err
			}
			return bumpCalendarCTag(tx, acc)
		})
	})
	return ev, rerr
}

func (Service) Update(ctx context.Context, acc *store.Account, id int64, in EventInput) (ev store.CalendarEvent, rerr error) {
	if !mox.CalendarEnabled() {
		return ev, ErrNotEnabled
	}

	acc.WithWLock(func() {
		rerr = acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			ev.ID = id
			if err := tx.Get(&ev); err != nil {
				return eventError(err)
			}
			if in.Revision != ev.Revision {
				return ErrStale
			}
			if in.UID == "" {
				in.UID = ev.UID
			} else if in.UID != ev.UID {
				return fmt.Errorf("%w: UID cannot be changed", ErrInvalid)
			}
			if in.RecurrenceID == "" && ev.RecurrenceID != "" {
				in.RecurrenceID = ev.RecurrenceID
			}
			if err := validateInput(in); err != nil {
				return err
			}

			series, err := eventsByUID(tx, in.UID)
			if err != nil {
				return err
			}
			existingExceptions, err := storedExceptionCount(series, ev.ID)
			if err != nil {
				return err
			}
			inputExceptions, err := inputExceptionCount(in)
			if err != nil {
				return err
			}
			if existingExceptions+inputExceptions > Limits().MaxExceptions {
				return fmt.Errorf("%w: too many exceptions", ErrLimit)
			}

			unknown := in.Unknown
			if unknown == nil {
				unknown = unknownProperties(ev.ICS)
			}
			updated := eventFromInput(in)
			updated.ID = ev.ID
			updated.CalendarID = ev.CalendarID
			updated.Created = ev.Created
			updated.LastModified = time.Now().UTC()
			updated.Revision = ev.Revision + 1
			updated.Sequence = ev.Sequence + 1
			updated.ICS, err = serializeEvent(updated, unknown)
			if err != nil {
				return err
			}
			if len(updated.ICS) > Limits().MaxICSBytes {
				return fmt.Errorf("%w: ICS is too large", ErrLimit)
			}
			if err := tx.Update(&updated); err != nil {
				return err
			}
			ev = updated
			return bumpCalendarCTag(tx, acc)
		})
	})
	return ev, rerr
}

func (Service) Respond(ctx context.Context, acc *store.Account, id int64, revision int64, attendeeAddress, partStat string) (ev store.CalendarEvent, rerr error) {
	if !mox.CalendarEnabled() {
		return ev, ErrNotEnabled
	}
	partStat = strings.ToUpper(partStat)
	switch partStat {
	case "ACCEPTED", "TENTATIVE", "DECLINED":
	default:
		return ev, ErrInvalid
	}

	acc.WithWLock(func() {
		rerr = acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			ev.ID = id
			if err := tx.Get(&ev); err != nil {
				return eventError(err)
			}
			if revision != ev.Revision {
				return ErrStale
			}

			found := false
			attendees := append([]store.CalendarAttendee(nil), ev.Attendees...)
			for i := range attendees {
				if calendarAddressMatch(attendees[i].Address, attendeeAddress) {
					attendees[i].PartStat = partStat
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: not an attendee", ErrInvalid)
			}

			unknown := unknownProperties(ev.ICS)
			updated := ev
			updated.Attendees = attendees
			updated.LastModified = time.Now().UTC()
			updated.Revision = ev.Revision + 1
			var err error
			updated.ICS, err = serializeEvent(updated, unknown)
			if err != nil {
				return err
			}
			if len(updated.ICS) > Limits().MaxICSBytes {
				return fmt.Errorf("%w: ICS is too large", ErrLimit)
			}
			if err := tx.Update(&updated); err != nil {
				return err
			}
			ev = updated
			return bumpCalendarCTag(tx, acc)
		})
	})
	return ev, rerr
}

func calendarAddressMatch(a, b string) bool {
	a = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(a)), "mailto:")
	b = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(b)), "mailto:")
	return a == b
}

func (Service) Delete(ctx context.Context, acc *store.Account, id int64, revision int64, wholeSeries bool) (rerr error) {
	if !mox.CalendarEnabled() {
		return ErrNotEnabled
	}
	acc.WithWLock(func() {
		rerr = acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			ev := store.CalendarEvent{ID: id}
			if err := tx.Get(&ev); err != nil {
				return eventError(err)
			}
			if revision != ev.Revision {
				return ErrStale
			}
			if wholeSeries || ev.RecurrenceID == "" {
				_, err := bstore.QueryTx[store.CalendarEvent](tx).FilterNonzero(store.CalendarEvent{UID: ev.UID}).Delete()
				if err != nil {
					return err
				}
				return bumpCalendarCTag(tx, acc)
			}
			if err := tx.Delete(&ev); err != nil {
				return err
			}
			return bumpCalendarCTag(tx, acc)
		})
	})
	return rerr
}

func (Service) Get(ctx context.Context, acc *store.Account, id int64) (ev store.CalendarEvent, err error) {
	if !mox.CalendarEnabled() {
		return ev, ErrNotEnabled
	}
	err = acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		ev.ID = id
		return eventError(tx.Get(&ev))
	})
	return ev, err
}

func (Service) Query(ctx context.Context, acc *store.Account, from, to time.Time) ([]Instance, error) {
	if !mox.CalendarEnabled() {
		return nil, ErrNotEnabled
	}
	maxTo := from.AddDate(0, 0, Limits().ExpansionHorizonDays)
	if to.After(maxTo) {
		to = maxTo
	}
	return query(ctx, acc, from, to)
}

func (Service) GetObject(ctx context.Context, acc *store.Account, uid string) (obj CalendarObject, err error) {
	if !mox.CalendarEnabled() {
		return obj, ErrNotEnabled
	}
	if uid == "" {
		return obj, ErrNotFound
	}
	err = acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		events, err := eventsByUID(tx, uid)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return ErrNotFound
		}
		obj = calendarObject(uid, events)
		return nil
	})
	return obj, err
}

func (Service) ListObjects(ctx context.Context, acc *store.Account) ([]CalendarObject, error) {
	if !mox.CalendarEnabled() {
		return nil, ErrNotEnabled
	}
	var objs []CalendarObject
	err := acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		cal, err := acc.DefaultCalendar(tx)
		if err != nil {
			return err
		}
		events, err := bstore.QueryTx[store.CalendarEvent](tx).FilterNonzero(store.CalendarEvent{CalendarID: cal.ID}).List()
		if err != nil {
			return err
		}
		byUID := map[string][]store.CalendarEvent{}
		var uids []string
		for _, ev := range events {
			if _, ok := byUID[ev.UID]; !ok {
				uids = append(uids, ev.UID)
			}
			byUID[ev.UID] = append(byUID[ev.UID], ev)
		}
		sort.Strings(uids)
		for _, uid := range uids {
			objs = append(objs, calendarObject(uid, byUID[uid]))
		}
		return nil
	})
	return objs, err
}

func (Service) PutObject(ctx context.Context, acc *store.Account, uid string, ics []byte, ifMatch string) (obj CalendarObject, rerr error) {
	if !mox.CalendarEnabled() {
		return obj, ErrNotEnabled
	}
	comps, err := objectVEVENTs(ics)
	if err != nil {
		return obj, err
	}
	if len(comps) > Limits().MaxComponents {
		return obj, fmt.Errorf("%w: too many components", ErrLimit)
	}

	var inputs []EventInput
	var fragments [][]byte
	seenRID := map[string]bool{}
	var exceptions int
	for _, comp := range comps {
		if comp.UID() != uid {
			return obj, fmt.Errorf("%w: UID mismatch", ErrInvalid)
		}
		in, err := eventInputFromComponent(comp)
		if err != nil {
			return obj, err
		}
		if err := validateInput(in); err != nil {
			return obj, err
		}
		if seenRID[in.RecurrenceID] {
			return obj, fmt.Errorf("%w: duplicate RECURRENCE-ID", ErrInvalid)
		}
		seenRID[in.RecurrenceID] = true
		n, err := inputExceptionCount(in)
		if err != nil {
			return obj, err
		}
		exceptions += n
		frag := comp.Serialize()
		if len(frag) > Limits().MaxICSBytes {
			return obj, fmt.Errorf("%w: ICS is too large", ErrLimit)
		}
		inputs = append(inputs, in)
		fragments = append(fragments, frag)
	}
	if exceptions > Limits().MaxExceptions {
		return obj, fmt.Errorf("%w: too many exceptions", ErrLimit)
	}

	acc.WithWLock(func() {
		rerr = acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			calendar, err := acc.DefaultCalendar(tx)
			if err != nil {
				return err
			}
			existing, err := eventsByUID(tx, uid)
			if err != nil {
				return err
			}
			revision := int64(1)
			if len(existing) == 0 {
				if ifMatch != "" && ifMatch != "*" {
					return ErrNotFound
				}
			} else {
				if ifMatch == "*" {
					return ErrStale
				}
				if ifMatch != "" && ifMatch != objectETag(existing) {
					return ErrStale
				}
				for _, ev := range existing {
					if ev.Revision >= revision {
						revision = ev.Revision + 1
					}
				}
				if _, err := bstore.QueryTx[store.CalendarEvent](tx).FilterNonzero(store.CalendarEvent{UID: uid}).Delete(); err != nil {
					return err
				}
			}

			now := time.Now().UTC()
			inserted := make([]store.CalendarEvent, 0, len(inputs))
			for i, in := range inputs {
				ev := eventFromInput(in)
				ev.CalendarID = calendar.ID
				ev.Revision = revision
				ev.Created = now
				ev.LastModified = now
				ev.ICS = fragments[i]
				if err := tx.Insert(&ev); err != nil {
					return err
				}
				inserted = append(inserted, ev)
			}
			if err := bumpCalendarCTag(tx, acc); err != nil {
				return err
			}
			obj = calendarObject(uid, inserted)
			return nil
		})
	})
	return obj, rerr
}

func (Service) DeleteObject(ctx context.Context, acc *store.Account, uid string, ifMatch string) (rerr error) {
	if !mox.CalendarEnabled() {
		return ErrNotEnabled
	}
	if uid == "" {
		return ErrNotFound
	}
	acc.WithWLock(func() {
		rerr = acc.DB.Write(ctx, func(tx *bstore.Tx) error {
			events, err := eventsByUID(tx, uid)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return ErrNotFound
			}
			if ifMatch != "" && ifMatch != objectETag(events) {
				return ErrStale
			}
			if _, err := bstore.QueryTx[store.CalendarEvent](tx).FilterNonzero(store.CalendarEvent{UID: uid}).Delete(); err != nil {
				return err
			}
			return bumpCalendarCTag(tx, acc)
		})
	})
	return rerr
}

func validateInput(in EventInput) error {
	if in.Start.UTC.IsZero() || in.End.UTC.IsZero() {
		return fmt.Errorf("%w: start and end are required", ErrInvalid)
	}
	if in.Start.DateOnly != in.End.DateOnly {
		return fmt.Errorf("%w: start and end value types differ", ErrInvalid)
	}
	if in.Start.TZID != in.End.TZID {
		return fmt.Errorf("%w: start and end TZID differ", ErrInvalid)
	}
	if in.Start.Floating != in.End.Floating {
		return fmt.Errorf("%w: start and end floating differ", ErrInvalid)
	}
	if in.Start.DateOnly {
		if in.End.UTC.Before(in.Start.UTC) {
			return fmt.Errorf("%w: end is before start", ErrInvalid)
		}
	} else if !in.End.UTC.After(in.Start.UTC) {
		return fmt.Errorf("%w: end must be after start", ErrInvalid)
	}
	if len(in.Attendees) > Limits().MaxAttendees {
		return fmt.Errorf("%w: too many attendees", ErrLimit)
	}
	if err := validateRecurrence(in); err != nil {
		return err
	}
	for _, p := range in.Unknown {
		name := strings.ToUpper(p.Name)
		if !validPropertyName(p.Name) {
			return fmt.Errorf("%w: invalid property name %q", ErrInvalid, p.Name)
		}
		if canonicalProperty[name] || name == "BEGIN" || name == "END" {
			return fmt.Errorf("%w: property %s is not an extra VEVENT property", ErrInvalid, p.Name)
		}
		for _, param := range p.Params {
			for _, value := range param.Values {
				if strings.EqualFold(param.Name, "RANGE") && strings.EqualFold(value, "THISANDFUTURE") {
					return fmt.Errorf("%w: THISANDFUTURE is not supported", ErrInvalid)
				}
			}
		}
	}
	return nil
}

func validateRecurrence(in EventInput) error {
	var recurrence icalendar.Recurrence
	var err error
	if in.RRULE != "" {
		recurrence, err = icalendar.ParseRRULE(in.RRULE)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if recurrence.Count > Limits().MaxRecurrenceInstances {
			return fmt.Errorf("%w: recurrence COUNT is too large", ErrLimit)
		}
	}
	rdates, err := parseDateValues("RDATE", in.RDATE)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	exdates, err := parseDateValues("EXDATE", in.EXDATE)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	limits := Limits()
	if len(rdates) > limits.MaxRecurrenceInstances {
		return fmt.Errorf("%w: too many RDATE values", ErrLimit)
	}
	if recurrence.Count > 0 && recurrence.Count+len(rdates) > limits.MaxRecurrenceInstances {
		return fmt.Errorf("%w: recurrence set is too large", ErrLimit)
	}
	if in.RRULE == "" {
		return nil
	}
	to := in.Start.UTC.AddDate(0, 0, limits.ExpansionHorizonDays)
	expandRDates := rdates
	expandMax := limits.MaxRecurrenceInstances
	if recurrence.Freq != "" && recurrence.Count == 0 {
		// Every RDATE counts against the cap, including values outside the
		// expansion horizon. Expand only the rule with the remaining budget.
		expandRDates = nil
		expandMax -= len(rdates)
	}
	_, err = icalendar.Expand(in.Start, recurrence, expandRDates, exdates, in.Start.UTC, to, expandMax)
	if errors.Is(err, icalendar.ErrExpansionLimit) {
		return fmt.Errorf("%w: recurrence expansion is too large", ErrLimit)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func eventFromInput(in EventInput) store.CalendarEvent {
	return store.CalendarEvent{
		UID:          in.UID,
		RecurrenceID: in.RecurrenceID,
		Sequence:     in.Sequence,
		Organizer:    in.Organizer,
		Summary:      in.Summary,
		Description:  in.Description,
		Location:     in.Location,
		Status:       in.Status,
		Class:        in.Class,
		StartUTC:     in.Start.UTC,
		EndUTC:       in.End.UTC,
		TZID:         in.Start.TZID,
		DateOnly:     in.Start.DateOnly,
		Floating:     in.Start.Floating,
		RRULE:        in.RRULE,
		RDATE:        append([]string(nil), in.RDATE...),
		EXDATE:       append([]string(nil), in.EXDATE...),
		Attendees:    append([]store.CalendarAttendee(nil), in.Attendees...),
	}
}

func serializeEvent(ev store.CalendarEvent, unknown []icalendar.Property) ([]byte, error) {
	props := []icalendar.Property{
		{Name: "UID", Value: ev.UID},
		{Name: "SEQUENCE", Value: strconv.Itoa(ev.Sequence)},
		icalendar.DateTime{UTC: ev.StartUTC, TZID: ev.TZID, DateOnly: ev.DateOnly, Floating: ev.Floating}.Property("DTSTART"),
		icalendar.DateTime{UTC: ev.EndUTC, TZID: ev.TZID, DateOnly: ev.DateOnly, Floating: ev.Floating}.Property("DTEND"),
	}
	add := func(name, value string) {
		if value != "" {
			props = append(props, icalendar.Property{Name: name, Value: value})
		}
	}
	add("RECURRENCE-ID", ev.RecurrenceID)
	add("SUMMARY", ev.Summary)
	add("DESCRIPTION", ev.Description)
	add("LOCATION", ev.Location)
	add("STATUS", ev.Status)
	add("CLASS", ev.Class)
	add("ORGANIZER", ev.Organizer)
	add("RRULE", ev.RRULE)
	for _, value := range ev.RDATE {
		add("RDATE", value)
	}
	for _, value := range ev.EXDATE {
		add("EXDATE", value)
	}
	for _, attendee := range ev.Attendees {
		p := icalendar.Property{Name: "ATTENDEE", Value: attendee.Address}
		param := func(name, value string) {
			if value != "" {
				p.Params = append(p.Params, icalendar.Param{Name: name, Values: []string{value}})
			}
		}
		param("CN", attendee.Name)
		param("ROLE", attendee.Role)
		param("PARTSTAT", attendee.PartStat)
		if attendee.RSVP {
			param("RSVP", "TRUE")
		}
		props = append(props, p)
	}
	props = append(props, unknown...)
	intendedNames := propertyNameCounts(props)
	full := icalendar.Calendar{Components: []icalendar.Component{{Name: "VEVENT", Properties: props}}}.Serialize()
	parsed, err := icalendar.Parse(full)
	if err != nil || len(parsed.Components) != 1 || parsed.Components[0].Name != "VEVENT" || len(parsed.Components[0].Components) != 0 {
		return nil, fmt.Errorf("%w: invalid serialized VEVENT", ErrInvalid)
	}
	if !samePropertyNames(intendedNames, propertyNameCounts(parsed.Components[0].Properties)) {
		return nil, fmt.Errorf("%w: serialized VEVENT properties changed", ErrInvalid)
	}
	const prefix = "BEGIN:VCALENDAR\r\n"
	const suffix = "END:VCALENDAR\r\n"
	if !strings.HasPrefix(string(full), prefix) || !strings.HasSuffix(string(full), suffix) {
		return nil, fmt.Errorf("%w: could not serialize VEVENT", ErrInvalid)
	}
	return full[len(prefix) : len(full)-len(suffix)], nil
}

func unknownProperties(ics []byte) []icalendar.Property {
	wrapped := append([]byte("BEGIN:VCALENDAR\r\n"), ics...)
	wrapped = append(wrapped, []byte("END:VCALENDAR\r\n")...)
	cal, err := icalendar.Parse(wrapped)
	if err != nil || len(cal.Components) != 1 {
		return nil
	}
	var unknown []icalendar.Property
	for _, p := range cal.Components[0].Properties {
		if !canonicalProperty[p.Name] {
			unknown = append(unknown, p)
		}
	}
	return unknown
}

var canonicalProperty = map[string]bool{
	"UID": true, "RECURRENCE-ID": true, "SEQUENCE": true, "SUMMARY": true,
	"DESCRIPTION": true, "LOCATION": true, "STATUS": true, "CLASS": true,
	"ORGANIZER": true, "DTSTART": true, "DTEND": true, "RRULE": true,
	"RDATE": true, "EXDATE": true, "ATTENDEE": true,
}

func validPropertyName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func propertyNameCounts(properties []icalendar.Property) map[string]int {
	counts := map[string]int{}
	for _, p := range properties {
		counts[strings.ToUpper(p.Name)]++
	}
	return counts
}

func samePropertyNames(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for name, count := range a {
		if b[name] != count {
			return false
		}
	}
	return true
}

func newUID() (string, error) {
	var random [16]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generating event uid: %w", err)
	}
	return fmt.Sprintf("%x@mox", random[:]), nil
}

func eventsByUID(tx *bstore.Tx, uid string) ([]store.CalendarEvent, error) {
	return bstore.QueryTx[store.CalendarEvent](tx).FilterNonzero(store.CalendarEvent{UID: uid}).List()
}

func storedExceptionCount(events []store.CalendarEvent, exceptID int64) (int, error) {
	n := 0
	for _, ev := range events {
		if ev.ID == exceptID {
			continue
		}
		if ev.RecurrenceID != "" {
			n++
		}
		exdates, err := parseDateValues("EXDATE", ev.EXDATE)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		n += len(exdates)
	}
	return n, nil
}

func inputExceptionCount(in EventInput) (int, error) {
	exdates, err := parseDateValues("EXDATE", in.EXDATE)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	n := len(exdates)
	if in.RecurrenceID != "" {
		n++
	}
	return n, nil
}

func parseDateValues(name string, values []string) ([]icalendar.DateTime, error) {
	var dates []icalendar.DateTime
	for _, list := range values {
		for _, value := range strings.Split(list, ",") {
			p := icalendar.Property{Name: name, Value: value}
			if len(value) == len("20060102") {
				p.Params = []icalendar.Param{{Name: "VALUE", Values: []string{"DATE"}}}
			}
			date, err := icalendar.ParseDateTime(p)
			if err != nil {
				return nil, err
			}
			dates = append(dates, date)
		}
	}
	return dates, nil
}

func eventError(err error) error {
	if errors.Is(err, bstore.ErrAbsent) {
		return ErrNotFound
	}
	return err
}

func bumpCalendarCTag(tx *bstore.Tx, acc *store.Account) error {
	c, err := acc.DefaultCalendar(tx)
	if err != nil {
		return err
	}
	c.CTag++
	return tx.Update(&c)
}

func objectVEVENTs(ics []byte) ([]icalendar.Component, error) {
	cal, err := icalendar.Parse(ics)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var vevents []icalendar.Component
	for _, c := range cal.Components {
		switch c.Name {
		case "VEVENT":
			vevents = append(vevents, c)
		case "VTIMEZONE":
			continue
		default:
			return nil, fmt.Errorf("%w: unsupported component %s", ErrInvalid, c.Name)
		}
	}
	if len(vevents) == 0 {
		return nil, fmt.Errorf("%w: no VEVENT", ErrInvalid)
	}
	if _, _, err := icalendar.SplitSeries(vevents); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return vevents, nil
}

func eventInputFromComponent(comp icalendar.Component) (EventInput, error) {
	in := EventInput{
		UID:      comp.UID(),
		Sequence: comp.Sequence(),
	}
	if p, ok := comp.Prop("RECURRENCE-ID"); ok {
		in.RecurrenceID = p.Value
	}
	if p, ok := comp.Prop("SUMMARY"); ok {
		in.Summary = p.Value
	}
	if p, ok := comp.Prop("DESCRIPTION"); ok {
		in.Description = p.Value
	}
	if p, ok := comp.Prop("LOCATION"); ok {
		in.Location = p.Value
	}
	if p, ok := comp.Prop("STATUS"); ok {
		in.Status = p.Value
	}
	if p, ok := comp.Prop("CLASS"); ok {
		in.Class = p.Value
	}
	if p, ok := comp.Prop("ORGANIZER"); ok {
		in.Organizer = p.Value
	}
	if p, ok := comp.Prop("RRULE"); ok {
		in.RRULE = p.Value
	}

	startProp, ok := comp.Prop("DTSTART")
	if !ok {
		return EventInput{}, fmt.Errorf("%w: DTSTART is required", ErrInvalid)
	}
	start, err := icalendar.ParseDateTime(startProp)
	if err != nil {
		return EventInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	in.Start = start

	if p, ok := comp.Prop("DTEND"); ok {
		end, err := icalendar.ParseDateTime(p)
		if err != nil {
			return EventInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		in.End = end
	} else if p, ok := comp.Prop("DURATION"); ok {
		d, err := parseICSDuration(p.Value)
		if err != nil {
			return EventInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		end := start
		end.UTC = start.UTC.Add(d)
		in.End = end
	} else if start.DateOnly {
		in.End = start
	} else {
		return EventInput{}, fmt.Errorf("%w: DTEND or DURATION is required", ErrInvalid)
	}

	for _, p := range comp.Props("RDATE") {
		in.RDATE = append(in.RDATE, p.Value)
	}
	for _, p := range comp.Props("EXDATE") {
		in.EXDATE = append(in.EXDATE, p.Value)
	}
	for _, p := range comp.Props("ATTENDEE") {
		att := store.CalendarAttendee{Address: p.Value}
		if v, ok := p.Param("CN"); ok {
			att.Name = v
		}
		if v, ok := p.Param("ROLE"); ok {
			att.Role = v
		}
		if v, ok := p.Param("PARTSTAT"); ok {
			att.PartStat = v
		}
		if v, ok := p.Param("RSVP"); ok {
			att.RSVP = strings.EqualFold(v, "TRUE")
		}
		in.Attendees = append(in.Attendees, att)
	}
	for _, p := range comp.Properties {
		name := strings.ToUpper(p.Name)
		if canonicalProperty[name] {
			continue
		}
		in.Unknown = append(in.Unknown, p)
	}
	return in, nil
}

func parseICSDuration(s string) (time.Duration, error) {
	orig := s
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	neg := false
	switch s[0] {
	case '-':
		neg = true
		s = s[1:]
	case '+':
		s = s[1:]
	}
	if len(s) < 2 || s[0] != 'P' {
		return 0, fmt.Errorf("invalid duration %q", orig)
	}
	s = s[1:]
	var total time.Duration
	inTime := false
	sawWeek := false
	sawDate := false
	for len(s) > 0 {
		if s[0] == 'T' {
			if inTime {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			inTime = true
			s = s[1:]
			if s == "" {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			continue
		}
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 || i >= len(s) {
			return 0, fmt.Errorf("invalid duration %q", orig)
		}
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", orig)
		}
		unit := s[i]
		s = s[i+1:]
		switch unit {
		case 'W':
			if inTime || sawDate || sawWeek {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			sawWeek = true
			total += time.Duration(n) * 7 * 24 * time.Hour
		case 'D':
			if inTime || sawWeek {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			sawDate = true
			total += time.Duration(n) * 24 * time.Hour
		case 'H':
			if !inTime || sawWeek {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			total += time.Duration(n) * time.Hour
		case 'M':
			if !inTime || sawWeek {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			total += time.Duration(n) * time.Minute
		case 'S':
			if !inTime || sawWeek {
				return 0, fmt.Errorf("invalid duration %q", orig)
			}
			total += time.Duration(n) * time.Second
		default:
			return 0, fmt.Errorf("invalid duration %q", orig)
		}
	}
	if neg {
		total = -total
	}
	return total, nil
}

func objectETag(events []store.CalendarEvent) string {
	var max int64
	for _, ev := range events {
		if ev.Revision > max {
			max = ev.Revision
		}
	}
	return fmt.Sprintf(`"rev-%d"`, max)
}

func wrapICS(fragments [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//mox//caldav//EN\r\n")
	for _, f := range fragments {
		buf.Write(f)
	}
	buf.WriteString("END:VCALENDAR\r\n")
	return buf.Bytes()
}

func calendarObject(uid string, events []store.CalendarEvent) CalendarObject {
	events = append([]store.CalendarEvent(nil), events...)
	sort.Slice(events, func(i, j int) bool {
		return events[i].RecurrenceID < events[j].RecurrenceID
	})
	fragments := make([][]byte, len(events))
	for i, ev := range events {
		fragments[i] = ev.ICS
	}
	return CalendarObject{
		UID:  uid,
		ETag: objectETag(events),
		ICS:  wrapICS(fragments),
	}
}
