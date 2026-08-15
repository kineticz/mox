package icalendar

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrExpansionLimit is returned when an expansion would emit more than max instances.
var ErrExpansionLimit = errors.New("recurrence expansion exceeds limit")

// Recurrence is the supported subset of an iCalendar recurrence rule.
type Recurrence struct {
	Freq          string
	Interval      int
	Count         int
	Until         *time.Time
	ByDay         []WeekdayNum
	ByMonthDay    []int
	UntilDateOnly bool
}

// WeekdayNum identifies a weekday, optionally by its ordinal in a month.
// Offset 0 means every occurrence of the weekday; -1 means the last.
type WeekdayNum struct {
	Offset int
	Day    time.Weekday
}

// ParseRRULE parses the recurrence rule subset supported by this package.
func ParseRRULE(value string) (Recurrence, error) {
	r := Recurrence{Interval: 1}
	seen := map[string]bool{}
	for _, field := range strings.Split(value, ";") {
		key, raw, ok := strings.Cut(field, "=")
		key = strings.ToUpper(key)
		if !ok || key == "" || raw == "" {
			return Recurrence{}, fmt.Errorf("icalendar: invalid RRULE field %q", field)
		}
		if seen[key] {
			return Recurrence{}, fmt.Errorf("icalendar: duplicate RRULE field %q", key)
		}
		seen[key] = true

		var err error
		switch key {
		case "FREQ":
			r.Freq = strings.ToUpper(raw)
			switch r.Freq {
			case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
			default:
				err = fmt.Errorf("unsupported FREQ %q", raw)
			}
		case "INTERVAL":
			r.Interval, err = positiveInt(raw)
		case "COUNT":
			r.Count, err = positiveInt(raw)
		case "UNTIL":
			var until time.Time
			if len(raw) == len("20060102") {
				until, err = time.Parse("20060102", raw)
				r.UntilDateOnly = err == nil
			} else if strings.HasSuffix(raw, "Z") {
				until, err = time.Parse(dateTimeLayout+"Z", raw)
			} else {
				until, err = time.ParseInLocation(dateTimeLayout, raw, time.UTC)
			}
			if err == nil {
				until = until.UTC()
				r.Until = &until
			}
		case "BYDAY":
			for _, part := range strings.Split(raw, ",") {
				day, parseErr := parseWeekdayNum(part)
				if parseErr != nil {
					err = parseErr
					break
				}
				r.ByDay = append(r.ByDay, day)
			}
		case "BYMONTHDAY":
			for _, part := range strings.Split(raw, ",") {
				day, parseErr := strconv.Atoi(part)
				if parseErr != nil || day == 0 || day < -31 || day > 31 {
					err = fmt.Errorf("invalid BYMONTHDAY %q", part)
					break
				}
				r.ByMonthDay = append(r.ByMonthDay, day)
			}
		case "WKST":
			if strings.ToUpper(raw) != "MO" {
				err = fmt.Errorf("unsupported WKST %q", raw)
			}
		default:
			err = fmt.Errorf("unsupported RRULE field %q", key)
		}
		if err != nil {
			return Recurrence{}, fmt.Errorf("icalendar: parsing RRULE: %w", err)
		}
	}

	if r.Freq == "" {
		return Recurrence{}, fmt.Errorf("icalendar: RRULE has no FREQ")
	}
	if r.Count != 0 && r.Until != nil {
		return Recurrence{}, fmt.Errorf("icalendar: RRULE cannot have both COUNT and UNTIL")
	}
	if len(r.ByDay) > 0 {
		if r.Freq != "WEEKLY" && r.Freq != "MONTHLY" {
			return Recurrence{}, fmt.Errorf("icalendar: BYDAY is unsupported with FREQ=%s", r.Freq)
		}
		if r.Freq == "WEEKLY" {
			for _, day := range r.ByDay {
				if day.Offset != 0 {
					return Recurrence{}, fmt.Errorf("icalendar: ordinal BYDAY is unsupported with FREQ=WEEKLY")
				}
			}
		}
	}
	if len(r.ByMonthDay) > 0 && r.Freq != "MONTHLY" && r.Freq != "YEARLY" {
		return Recurrence{}, fmt.Errorf("icalendar: BYMONTHDAY is unsupported with FREQ=%s", r.Freq)
	}
	return r, nil
}

func positiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("expected positive integer, got %q", value)
	}
	return n, nil
}

func parseWeekdayNum(value string) (WeekdayNum, error) {
	if len(value) < 2 {
		return WeekdayNum{}, fmt.Errorf("invalid BYDAY %q", value)
	}
	name := strings.ToUpper(value[len(value)-2:])
	days := map[string]time.Weekday{
		"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
		"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
	}
	day, ok := days[name]
	if !ok {
		return WeekdayNum{}, fmt.Errorf("invalid BYDAY %q", value)
	}
	offset := 0
	if prefix := value[:len(value)-2]; prefix != "" {
		var err error
		offset, err = strconv.Atoi(prefix)
		if err != nil || offset == 0 || offset < -53 || offset > 53 {
			return WeekdayNum{}, fmt.Errorf("invalid BYDAY %q", value)
		}
	}
	return WeekdayNum{Offset: offset, Day: day}, nil
}

// RecurrenceSet returns the RRULE, RDATEs, and EXDATEs on c.
func (c Component) RecurrenceSet() (rrule Recurrence, rdates, exdates []DateTime, err error) {
	rules := c.Props("RRULE")
	if len(rules) > 1 {
		return Recurrence{}, nil, nil, fmt.Errorf("icalendar: multiple RRULE properties")
	}
	if len(rules) == 1 {
		rrule, err = ParseRRULE(rules[0].Value)
		if err != nil {
			return Recurrence{}, nil, nil, err
		}
	}
	parseDates := func(name string) ([]DateTime, error) {
		var dates []DateTime
		for _, p := range c.Props(name) {
			for _, value := range strings.Split(p.Value, ",") {
				item := p
				item.Value = value
				d, parseErr := ParseDateTime(item)
				if parseErr != nil {
					return nil, parseErr
				}
				dates = append(dates, d)
			}
		}
		return dates, nil
	}
	rdates, err = parseDates("RDATE")
	if err != nil {
		return Recurrence{}, nil, nil, err
	}
	exdates, err = parseDates("EXDATE")
	if err != nil {
		return Recurrence{}, nil, nil, err
	}
	return rrule, rdates, exdates, nil
}

// Expand returns recurrence starts in the half-open interval [from, to).
func Expand(dtstart DateTime, r Recurrence, rdates, exdates []DateTime, from, to time.Time, max int) (instances []DateTime, err error) {
	exactExdates := map[int64]bool{}
	dateExdates := map[string]bool{}
	for _, d := range exdates {
		if d.DateOnly {
			dateExdates[d.UTC.UTC().Format("20060102")] = true
		} else {
			exactExdates[d.UTC.UnixNano()] = true
		}
	}

	byInstant := map[int64]DateTime{}
	add := func(d DateTime) error {
		if d.UTC.Before(from) || !d.UTC.Before(to) || excluded(d, exactExdates, dateExdates) {
			return nil
		}
		key := d.UTC.UnixNano()
		if _, exists := byInstant[key]; exists {
			return nil
		}
		if len(byInstant) >= max {
			return ErrExpansionLimit
		}
		byInstant[key] = d
		return nil
	}

	if r.Freq == "" {
		if err := add(dtstart); err != nil {
			return nil, err
		}
	} else {
		if r.Interval <= 0 {
			r.Interval = 1
		}
		loc, err := recurrenceLocation(dtstart)
		if err != nil {
			return nil, err
		}
		startLocal := dtstart.UTC.In(loc)
		startDate := dateAtUTC(startLocal.Year(), startLocal.Month(), startLocal.Day())
		limitLocal := to.In(loc)
		endDate := dateAtUTC(limitLocal.Year(), limitLocal.Month(), limitLocal.Day()).AddDate(0, 0, 1)
		generated := 1
		if err := add(dtstart); err != nil {
			return nil, err
		}
		for date := startDate.AddDate(0, 0, 1); date.Before(endDate) && (r.Count == 0 || generated < r.Count); date = date.AddDate(0, 0, 1) {
			if !matchesRecurrenceDate(date, startDate, r) {
				continue
			}
			candidate, makeErr := recurrenceDateTime(dtstart, date, startLocal, loc)
			if makeErr != nil {
				return nil, makeErr
			}
			if candidate.UTC.Before(dtstart.UTC) {
				continue
			}
			if r.Until != nil && beyondUntil(candidate, date, r) {
				break
			}
			generated++
			if err := add(candidate); err != nil {
				return nil, err
			}
		}
	}
	for _, d := range rdates {
		if err := add(d); err != nil {
			return nil, err
		}
	}

	instances = make([]DateTime, 0, len(byInstant))
	for _, d := range byInstant {
		instances = append(instances, d)
	}
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].UTC.Before(instances[j].UTC)
	})
	return instances, nil
}

func recurrenceLocation(dtstart DateTime) (*time.Location, error) {
	if dtstart.TZID == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(dtstart.TZID)
	if err != nil {
		return nil, fmt.Errorf("icalendar: loading TZID %q: %w", dtstart.TZID, err)
	}
	return loc, nil
}

func dateAtUTC(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func matchesRecurrenceDate(date, start time.Time, r Recurrence) bool {
	days := int(date.Sub(start).Hours() / 24)
	switch r.Freq {
	case "DAILY":
		return days%r.Interval == 0
	case "WEEKLY":
		startMonday := start.AddDate(0, 0, -weekdayFromMonday(start.Weekday()))
		dateMonday := date.AddDate(0, 0, -weekdayFromMonday(date.Weekday()))
		weeks := int(dateMonday.Sub(startMonday).Hours() / (7 * 24))
		if weeks%r.Interval != 0 {
			return false
		}
		if len(r.ByDay) == 0 {
			return date.Weekday() == start.Weekday()
		}
		for _, byday := range r.ByDay {
			if date.Weekday() == byday.Day {
				return true
			}
		}
	case "MONTHLY":
		months := (date.Year()-start.Year())*12 + int(date.Month()-start.Month())
		if months%r.Interval != 0 {
			return false
		}
		return matchesMonthFilters(date, start, r)
	case "YEARLY":
		if (date.Year()-start.Year())%r.Interval != 0 {
			return false
		}
		if len(r.ByMonthDay) == 0 {
			return date.Month() == start.Month() && date.Day() == start.Day()
		}
		return matchesMonthDay(date, r.ByMonthDay)
	}
	return false
}

func weekdayFromMonday(day time.Weekday) int {
	return (int(day) + 6) % 7
}

func matchesMonthFilters(date, start time.Time, r Recurrence) bool {
	dayMatches := len(r.ByDay) == 0
	for _, byday := range r.ByDay {
		if date.Weekday() != byday.Day {
			continue
		}
		if byday.Offset == 0 || matchesWeekdayOrdinal(date, byday.Offset) {
			dayMatches = true
			break
		}
	}
	monthDayMatches := len(r.ByMonthDay) == 0 || matchesMonthDay(date, r.ByMonthDay)
	if len(r.ByDay) == 0 && len(r.ByMonthDay) == 0 {
		return date.Day() == start.Day()
	}
	return dayMatches && monthDayMatches
}

func matchesWeekdayOrdinal(date time.Time, offset int) bool {
	if offset > 0 {
		return (date.Day()-1)/7+1 == offset
	}
	last := daysInMonth(date.Year(), date.Month())
	return -((last-date.Day())/7 + 1) == offset
}

func matchesMonthDay(date time.Time, values []int) bool {
	last := daysInMonth(date.Year(), date.Month())
	for _, value := range values {
		day := value
		if day < 0 {
			day = last + day + 1
		}
		if date.Day() == day {
			return true
		}
	}
	return false
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func recurrenceDateTime(dtstart DateTime, date, startLocal time.Time, loc *time.Location) (DateTime, error) {
	d := dtstart
	if dtstart.DateOnly {
		d.UTC = date
		return d, nil
	}
	value := fmt.Sprintf("%04d%02d%02dT%02d%02d%02d",
		date.Year(), date.Month(), date.Day(), startLocal.Hour(), startLocal.Minute(), startLocal.Second())
	t, err := parseLocalDateTime(value, loc)
	if err != nil {
		return DateTime{}, err
	}
	d.UTC = t.UTC()
	return d, nil
}

func beyondUntil(candidate DateTime, date time.Time, r Recurrence) bool {
	if r.UntilDateOnly {
		untilDate := dateAtUTC(r.Until.Year(), r.Until.Month(), r.Until.Day())
		return date.After(untilDate)
	}
	return candidate.UTC.After(*r.Until)
}

func excluded(d DateTime, exact map[int64]bool, dates map[string]bool) bool {
	if exact[d.UTC.UnixNano()] {
		return true
	}
	loc := time.UTC
	if d.TZID != "" {
		if loaded, err := time.LoadLocation(d.TZID); err == nil {
			loc = loaded
		}
	}
	return dates[d.UTC.In(loc).Format("20060102")]
}
