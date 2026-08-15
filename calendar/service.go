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
				if exceptionCount(series)+len(master.EXDATE)+1 > Limits().MaxExceptions {
					return fmt.Errorf("%w: too many exceptions", ErrLimit)
				}
			} else if len(in.EXDATE)+exceptionCount(series) > Limits().MaxExceptions {
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
			exceptions := exceptionCountExcept(series, ev.ID)
			masterEXDATE := 0
			for _, item := range series {
				if item.ID != ev.ID && item.RecurrenceID == "" {
					masterEXDATE = len(item.EXDATE)
				}
			}
			if in.RecurrenceID == "" {
				masterEXDATE = len(in.EXDATE)
			} else {
				exceptions++
			}
			if masterEXDATE+exceptions > Limits().MaxExceptions {
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
	if in.RRULE != "" {
		if _, err := icalendar.ParseRRULE(in.RRULE); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	for _, p := range in.Unknown {
		if strings.EqualFold(p.Name, "VTODO") || strings.EqualFold(p.Name, "BEGIN") && strings.EqualFold(p.Value, "VTODO") {
			return fmt.Errorf("%w: VTODO is not supported", ErrInvalid)
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
	full := icalendar.Calendar{Components: []icalendar.Component{{Name: "VEVENT", Properties: props}}}.Serialize()
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
	known := map[string]bool{
		"UID": true, "RECURRENCE-ID": true, "SEQUENCE": true, "SUMMARY": true,
		"DESCRIPTION": true, "LOCATION": true, "STATUS": true, "CLASS": true,
		"ORGANIZER": true, "DTSTART": true, "DTEND": true, "RRULE": true,
		"RDATE": true, "EXDATE": true, "ATTENDEE": true,
	}
	var unknown []icalendar.Property
	for _, p := range cal.Components[0].Properties {
		if !known[p.Name] {
			unknown = append(unknown, p)
		}
	}
	return unknown
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

func exceptionCount(events []store.CalendarEvent) int {
	return exceptionCountExcept(events, 0)
}

func exceptionCountExcept(events []store.CalendarEvent, exceptID int64) int {
	n := 0
	for _, ev := range events {
		if ev.ID != exceptID && ev.RecurrenceID != "" {
			n++
		}
	}
	return n
}

func eventError(err error) error {
	if errors.Is(err, bstore.ErrAbsent) {
		return ErrNotFound
	}
	return err
}
