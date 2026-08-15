package webmail

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mjl-/mox/calendar"
	"github.com/mjl-/mox/icalendar"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
)

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
	ID            int64
	UID           string
	RecurrenceID  string
	Sequence      int
	Revision      int64
	Summary       string
	Description   string
	Location      string
	Status        string
	Organizer     string
	Start         CalendarTime
	End           CalendarTime
	RRULE         string
	RDATE         []string
	EXDATE        []string
	Attendees     []CalendarAttendeeView
	InstanceStart *CalendarTime
}

var calendarSvc calendar.Service

func (Webmail) CalendarEnabled(_ context.Context) bool {
	return mox.CalendarEnabled()
}

func (Webmail) CalendarQuery(ctx context.Context, from, to time.Time) []CalendarEventView {
	reqInfo := ctx.Value(requestInfoCtxKey).(requestInfo)
	acc := reqInfo.Account

	instances, err := calendarSvc.Query(ctx, acc, from, to)
	xcheckCalendar(ctx, err, "querying calendar")
	views := make([]CalendarEventView, len(instances))
	for i, inst := range instances {
		views[i] = instanceToView(inst)
	}
	return views
}

func (Webmail) CalendarGet(ctx context.Context, id int64) CalendarEventView {
	reqInfo := ctx.Value(requestInfoCtxKey).(requestInfo)
	acc := reqInfo.Account

	ev, err := calendarSvc.Get(ctx, acc, id)
	xcheckCalendar(ctx, err, "getting calendar event")
	return eventToView(ev)
}

func (Webmail) CalendarCreate(ctx context.Context, e CalendarEventView) CalendarEventView {
	reqInfo := ctx.Value(requestInfoCtxKey).(requestInfo)
	acc := reqInfo.Account

	ev, err := calendarSvc.Create(ctx, acc, eventInputFromView(e))
	xcheckCalendar(ctx, err, "creating calendar event")
	return eventToView(ev)
}

func (Webmail) CalendarUpdate(ctx context.Context, e CalendarEventView) CalendarEventView {
	reqInfo := ctx.Value(requestInfoCtxKey).(requestInfo)
	acc := reqInfo.Account

	ev, err := calendarSvc.Update(ctx, acc, e.ID, eventInputFromView(e))
	xcheckCalendar(ctx, err, "updating calendar event")
	return eventToView(ev)
}

func (Webmail) CalendarDelete(ctx context.Context, id int64, revision int64, wholeSeries bool) {
	reqInfo := ctx.Value(requestInfoCtxKey).(requestInfo)
	acc := reqInfo.Account

	err := calendarSvc.Delete(ctx, acc, id, revision, wholeSeries)
	xcheckCalendar(ctx, err, "deleting calendar event")
}

func (Webmail) CalendarRespond(ctx context.Context, id int64, revision int64, partStat string) CalendarEventView {
	reqInfo := ctx.Value(requestInfoCtxKey).(requestInfo)
	acc := reqInfo.Account

	if !mox.CalendarEnabled() {
		xcheckuserf(ctx, calendar.ErrNotEnabled, "responding to calendar event")
	}

	ev, err := calendarSvc.Get(ctx, acc, id)
	xcheckCalendar(ctx, err, "responding to calendar event")
	if calendarAddressMatch(ev.Organizer, reqInfo.LoginAddress) {
		xcheckuserf(ctx, errors.New("organizer cannot respond"), "responding to calendar event")
	}

	updated, err := calendarSvc.Respond(ctx, acc, id, revision, reqInfo.LoginAddress, partStat)
	xcheckCalendar(ctx, err, "responding to calendar event")
	return eventToView(updated)
}

func xcheckCalendar(ctx context.Context, err error, op string) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, calendar.ErrNotEnabled),
		errors.Is(err, calendar.ErrStale),
		errors.Is(err, calendar.ErrNotFound),
		errors.Is(err, calendar.ErrLimit),
		errors.Is(err, calendar.ErrInvalid):
		xcheckuserf(ctx, err, "%s", op)
	default:
		xcheckf(ctx, err, "%s", op)
	}
}

func calendarAddressMatch(a, b string) bool {
	a = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(a)), "mailto:")
	b = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(b)), "mailto:")
	return a == b
}

func eventToView(ev store.CalendarEvent) CalendarEventView {
	return CalendarEventView{
		ID:           ev.ID,
		UID:          ev.UID,
		RecurrenceID: ev.RecurrenceID,
		Sequence:     ev.Sequence,
		Revision:     ev.Revision,
		Summary:      ev.Summary,
		Description:  ev.Description,
		Location:     ev.Location,
		Status:       ev.Status,
		Organizer:    ev.Organizer,
		Start:        storeTimeToCalendarTime(ev.StartUTC, ev.TZID, ev.DateOnly),
		End:          storeTimeToCalendarTime(ev.EndUTC, ev.TZID, ev.DateOnly),
		RRULE:        ev.RRULE,
		RDATE:        append([]string(nil), ev.RDATE...),
		EXDATE:       append([]string(nil), ev.EXDATE...),
		Attendees:    attendeesToView(ev.Attendees),
	}
}

func instanceToView(inst calendar.Instance) CalendarEventView {
	v := eventToView(inst.Event)
	v.Start = dateTimeToCalendarTime(inst.Start)
	v.End = dateTimeToCalendarTime(inst.End)
	if inst.Event.RRULE != "" && inst.Event.RecurrenceID == "" {
		ct := dateTimeToCalendarTime(inst.Start)
		v.InstanceStart = &ct
	}
	return v
}

func eventInputFromView(e CalendarEventView) calendar.EventInput {
	return calendar.EventInput{
		UID:          e.UID,
		RecurrenceID: e.RecurrenceID,
		Sequence:     e.Sequence,
		Revision:     e.Revision,
		Summary:      e.Summary,
		Description:  e.Description,
		Location:     e.Location,
		Status:       e.Status,
		Organizer:    e.Organizer,
		Start:        calendarTimeToDateTime(e.Start),
		End:          calendarTimeToDateTime(e.End),
		RRULE:        e.RRULE,
		RDATE:        append([]string(nil), e.RDATE...),
		EXDATE:       append([]string(nil), e.EXDATE...),
		Attendees:    attendeesFromView(e.Attendees),
	}
}

func eventInputFromStore(ev store.CalendarEvent) calendar.EventInput {
	return calendar.EventInput{
		UID:          ev.UID,
		RecurrenceID: ev.RecurrenceID,
		Sequence:     ev.Sequence,
		Revision:     ev.Revision,
		Summary:      ev.Summary,
		Description:  ev.Description,
		Location:     ev.Location,
		Status:       ev.Status,
		Class:        ev.Class,
		Organizer:    ev.Organizer,
		Start:        storeStartToDateTime(ev),
		End:          storeEndToDateTime(ev),
		RRULE:        ev.RRULE,
		RDATE:        append([]string(nil), ev.RDATE...),
		EXDATE:       append([]string(nil), ev.EXDATE...),
		Attendees:    append([]store.CalendarAttendee(nil), ev.Attendees...),
	}
}

func storeStartToDateTime(ev store.CalendarEvent) icalendar.DateTime {
	return icalendar.DateTime{
		UTC:      ev.StartUTC,
		TZID:     ev.TZID,
		DateOnly: ev.DateOnly,
		Floating: ev.Floating,
	}
}

func storeEndToDateTime(ev store.CalendarEvent) icalendar.DateTime {
	return icalendar.DateTime{
		UTC:      ev.EndUTC,
		TZID:     ev.TZID,
		DateOnly: ev.DateOnly,
		Floating: ev.Floating,
	}
}

func storeTimeToCalendarTime(instant time.Time, tzid string, dateOnly bool) CalendarTime {
	return CalendarTime{
		Instant:  instant,
		TZID:     tzid,
		DateOnly: dateOnly,
	}
}

func dateTimeToCalendarTime(dt icalendar.DateTime) CalendarTime {
	return CalendarTime{
		Instant:  dt.UTC,
		TZID:     dt.TZID,
		DateOnly: dt.DateOnly,
	}
}

func calendarTimeToDateTime(ct CalendarTime) icalendar.DateTime {
	return icalendar.DateTime{
		UTC:      ct.Instant,
		TZID:     ct.TZID,
		DateOnly: ct.DateOnly,
	}
}

func attendeesToView(attendees []store.CalendarAttendee) []CalendarAttendeeView {
	if len(attendees) == 0 {
		return nil
	}
	views := make([]CalendarAttendeeView, len(attendees))
	for i, a := range attendees {
		views[i] = CalendarAttendeeView{
			Address:  a.Address,
			Name:     a.Name,
			Role:     a.Role,
			PartStat: a.PartStat,
		}
	}
	return views
}

func attendeesFromView(attendees []CalendarAttendeeView) []store.CalendarAttendee {
	if len(attendees) == 0 {
		return nil
	}
	out := make([]store.CalendarAttendee, len(attendees))
	for i, a := range attendees {
		out[i] = store.CalendarAttendee{
			Address:  a.Address,
			Name:     a.Name,
			Role:     a.Role,
			PartStat: a.PartStat,
		}
	}
	return out
}
