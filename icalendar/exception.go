package icalendar

import "fmt"

// SplitSeries separates VEVENT components into masters and exceptions keyed by UID.
//
// A master is a VEVENT with UID and no RECURRENCE-ID. An exception has UID and
// RECURRENCE-ID and is grouped under its UID.
func SplitSeries(comps []Component) (masters []Component, exceptions map[string][]Component, err error) {
	exceptions = map[string][]Component{}
	for _, c := range comps {
		if c.Name != "VEVENT" {
			continue
		}
		uid := c.UID()
		if uid == "" {
			return nil, nil, fmt.Errorf("icalendar: VEVENT without UID")
		}
		_, hasRID, parseErr := c.RecurrenceID()
		if parseErr != nil {
			return nil, nil, parseErr
		}
		if hasRID {
			exceptions[uid] = append(exceptions[uid], c)
			continue
		}
		masters = append(masters, c)
	}
	return masters, exceptions, nil
}
