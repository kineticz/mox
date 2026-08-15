package icalendar

import (
	"fmt"
	"strings"
	"time"
)

const dateTimeLayout = "20060102T150405"

// DateTime is an iCalendar DATE or DATE-TIME represented as a UTC instant,
// together with the information needed to preserve its original form.
type DateTime struct {
	UTC      time.Time
	TZID     string
	DateOnly bool
	Floating bool
}

// ParseDateTime parses an iCalendar DATE or DATE-TIME property.
func ParseDateTime(p Property) (DateTime, error) {
	switch p.valueType() {
	case vtDate:
		t, err := time.Parse("20060102", p.Value)
		if err != nil {
			return DateTime{}, fmt.Errorf("icalendar: parsing DATE %q: %w", p.Value, err)
		}
		return DateTime{UTC: t.UTC(), DateOnly: true}, nil

	case vtDateTime:
		tzid, hasTZID := p.Param("TZID")
		isUTC := strings.HasSuffix(p.Value, "Z")
		if isUTC {
			if hasTZID {
				return DateTime{}, fmt.Errorf("icalendar: DATE-TIME %q has both TZID and Z", p.Value)
			}
			t, err := time.Parse(dateTimeLayout+"Z", p.Value)
			if err != nil {
				return DateTime{}, fmt.Errorf("icalendar: parsing UTC DATE-TIME %q: %w", p.Value, err)
			}
			return DateTime{UTC: t.UTC()}, nil
		}

		if hasTZID {
			if tzid == "" {
				return DateTime{}, fmt.Errorf("icalendar: empty TZID")
			}
			loc, err := time.LoadLocation(tzid)
			if err != nil {
				return DateTime{}, fmt.Errorf("icalendar: loading TZID %q: %w", tzid, err)
			}
			t, err := parseLocalDateTime(p.Value, loc)
			if err != nil {
				return DateTime{}, fmt.Errorf("icalendar: parsing DATE-TIME %q in %q: %w", p.Value, tzid, err)
			}
			return DateTime{UTC: t.UTC(), TZID: tzid}, nil
		}

		t, err := time.ParseInLocation(dateTimeLayout, p.Value, time.UTC)
		if err != nil {
			return DateTime{}, fmt.Errorf("icalendar: parsing floating DATE-TIME %q: %w", p.Value, err)
		}
		return DateTime{UTC: t.UTC(), Floating: true}, nil

	default:
		return DateTime{}, fmt.Errorf("icalendar: property %q has value type %q, not DATE or DATE-TIME", p.Name, p.valueType())
	}
}

// parseLocalDateTime resolves overlaps and gaps as required by RFC 5545
// section 3.3.5: use the first overlap occurrence and the offset before a gap.
//
// ../rfc/5545:3.3.5
func parseLocalDateTime(value string, loc *time.Location) (time.Time, error) {
	wall, err := time.Parse(dateTimeLayout, value)
	if err != nil {
		return time.Time{}, err
	}

	// Any IANA UTC offset is within 24 hours, so transitions relevant to wall
	// occur in this wider window. ZoneBounds lets us collect every offset in
	// the window without relying on ParseInLocation's overlap/gap policy.
	windowStart := wall.Add(-48 * time.Hour)
	windowEnd := wall.Add(48 * time.Hour)
	offsets := map[int]bool{}
	for probe := windowStart; !probe.After(windowEnd); {
		localProbe := probe.In(loc)
		_, offset := localProbe.Zone()
		offsets[offset] = true
		_, zoneEnd := localProbe.ZoneBounds()
		if zoneEnd.IsZero() || zoneEnd.After(windowEnd) {
			break
		}
		probe = zoneEnd
	}

	var firstOccurrence time.Time
	var beforeGap time.Time
	var smallestForwardShift time.Duration
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(loc)
		localWall := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), 0, time.UTC)
		shift := localWall.Sub(wall)
		if shift == 0 {
			if firstOccurrence.IsZero() || candidate.Before(firstOccurrence) {
				firstOccurrence = candidate
			}
		} else if shift > 0 && (beforeGap.IsZero() || shift < smallestForwardShift) {
			// Applying the offset before a forward gap lands after the gap.
			beforeGap = candidate
			smallestForwardShift = shift
		}
	}
	if !firstOccurrence.IsZero() {
		return firstOccurrence, nil
	}
	if !beforeGap.IsZero() {
		return beforeGap, nil
	}
	return time.Time{}, fmt.Errorf("cannot resolve local time")
}

// Property returns d as an iCalendar property named name.
func (d DateTime) Property(name string) Property {
	p := Property{Name: name}
	if d.DateOnly {
		p.Params = []Param{{Name: "VALUE", Values: []string{"DATE"}}}
		p.Value = d.UTC.UTC().Format("20060102")
		return p
	}
	if d.TZID != "" {
		p.Params = []Param{{Name: "TZID", Values: []string{d.TZID}}}
		loc, err := time.LoadLocation(d.TZID)
		if err == nil {
			p.Value = d.UTC.In(loc).Format(dateTimeLayout)
		} else {
			p.Value = d.UTC.UTC().Format(dateTimeLayout)
		}
		return p
	}
	p.Value = d.UTC.UTC().Format(dateTimeLayout)
	if !d.Floating {
		p.Value += "Z"
	}
	return p
}
