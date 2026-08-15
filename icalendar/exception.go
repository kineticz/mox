package icalendar

// SplitSeries separates VEVENT components into masters and exceptions keyed by UID.
//
// A master is a VEVENT with no RECURRENCE-ID. An exception has RECURRENCE-ID and
// is grouped under its UID.
func SplitSeries(comps []Component) (masters []Component, exceptions map[string][]Component, err error) {
	exceptions = map[string][]Component{}
	for _, c := range comps {
		if c.Name != "VEVENT" {
			continue
		}
		_, hasRID, parseErr := c.RecurrenceID()
		if parseErr != nil {
			return nil, nil, parseErr
		}
		if hasRID {
			exceptions[c.UID()] = append(exceptions[c.UID()], c)
			continue
		}
		masters = append(masters, c)
	}
	return masters, exceptions, nil
}
