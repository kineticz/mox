package icalendar

import "strings"

// valueType is an iCalendar property value data type.
//
// ../rfc/5545:3.3
type valueType string

const (
	vtBinary      valueType = "BINARY"
	vtBoolean     valueType = "BOOLEAN"
	vtCalAddress  valueType = "CAL-ADDRESS"
	vtDate        valueType = "DATE"
	vtDateTime    valueType = "DATE-TIME"
	vtDuration    valueType = "DURATION"
	vtFloat       valueType = "FLOAT"
	vtInteger     valueType = "INTEGER"
	vtPeriod      valueType = "PERIOD"
	vtRecur       valueType = "RECUR"
	vtText        valueType = "TEXT"
	vtTime        valueType = "TIME"
	vtURI         valueType = "URI"
	vtUTCOffset   valueType = "UTC-OFFSET"
)

// defaultPropertyValueType maps IANA property names to their default value
// types. RFC 5545 section 3.2 defaults to TEXT for names not listed here.
//
// ../rfc/5545:3.7,3.8
// ../rfc/7986:5
var defaultPropertyValueType = map[string]valueType{
	// RFC 5545 calendar properties — non-TEXT defaults only.
	"ATTACH":           vtURI,
	"ATTENDEE":         vtCalAddress,
	"COMPLETED":        vtDateTime,
	"CREATED":          vtDateTime,
	"DTEND":            vtDateTime,
	"DTSTAMP":          vtDateTime,
	"DTSTART":          vtDateTime,
	"DUE":              vtDateTime,
	"DURATION":         vtDuration,
	"EXDATE":           vtDateTime,
	"EXRULE":           vtRecur,
	"FREEBUSY":         vtPeriod,
	"GEO":              vtFloat,
	"LAST-MODIFIED":    vtDateTime,
	"ORGANIZER":        vtCalAddress,
	"PERCENT-COMPLETE": vtInteger,
	"PRIORITY":         vtInteger,
	"RDATE":            vtDateTime,
	"RECURRENCE-ID":    vtDateTime,
	"REPEAT":           vtInteger,
	"RRULE":            vtRecur,
	"SEQUENCE":         vtInteger,
	"TRIGGER":          vtDuration,
	"TZOFFSETFROM":     vtUTCOffset,
	"TZOFFSETTO":       vtUTCOffset,
	"TZURL":            vtURI,
	"URL":              vtURI,

	// RFC 7986 extensions.
	"CONFERENCE":       vtURI,
	"IMAGE":            vtURI,
	"REFRESH-INTERVAL": vtDuration,
	"SOURCE":           vtURI,
}

func (p Property) valueType() valueType {
	if v, ok := p.Param("VALUE"); ok {
		return valueType(strings.ToUpper(v))
	}
	if vt, ok := defaultPropertyValueType[p.Name]; ok {
		return vt
	}
	// ../rfc/5545:3.2
	return vtText
}

func (p Property) textValue() bool {
	return p.valueType() == vtText
}
