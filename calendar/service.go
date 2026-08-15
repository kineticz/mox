package calendar

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
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
			if in.RecurrenceID != "" {
				var master *store.CalendarEvent
				for i := range series {
					if series[i].RecurrenceID == "" {
						master = &series[i]
						break
					}
				}
				if master == nil {
					return fmt.Errorf("%w: exception requires an existing master", ErrInvalid)
				}
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
			return nil
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
			return nil
		})
	})
	return ev, rerr
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
				return err
			}
			return tx.Delete(&ev)
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

func validateInput(in EventInput) error {
	if in.Start.UTC.IsZero() || in.End.UTC.IsZero() {
		return fmt.Errorf("%w: start and end are required", ErrInvalid)
	}
	if in.Start.DateOnly != in.End.DateOnly {
		return fmt.Errorf("%w: start and end value types differ", ErrInvalid)
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
