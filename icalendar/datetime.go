package icalendar

import (
	"fmt"
	"strings"
	"time"
)

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
			t, err := time.Parse("20060102T150405Z", p.Value)
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
			t, err := time.ParseInLocation("20060102T150405", p.Value, loc)
			if err != nil {
				return DateTime{}, fmt.Errorf("icalendar: parsing DATE-TIME %q in %q: %w", p.Value, tzid, err)
			}
			return DateTime{UTC: t.UTC(), TZID: tzid}, nil
		}

		t, err := time.ParseInLocation("20060102T150405", p.Value, time.UTC)
		if err != nil {
			return DateTime{}, fmt.Errorf("icalendar: parsing floating DATE-TIME %q: %w", p.Value, err)
		}
		return DateTime{UTC: t.UTC(), Floating: true}, nil

	default:
		return DateTime{}, fmt.Errorf("icalendar: property %q has value type %q, not DATE or DATE-TIME", p.Name, p.valueType())
	}
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
			p.Value = d.UTC.In(loc).Format("20060102T150405")
		} else {
			p.Value = d.UTC.UTC().Format("20060102T150405")
		}
		return p
	}
	p.Value = d.UTC.UTC().Format("20060102T150405")
	if !d.Floating {
		p.Value += "Z"
	}
	return p
}
