package icalendar

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Param is an iCalendar property parameter.
type Param struct {
	Name   string
	Values []string
}

// Property is a single iCalendar content line property.
type Property struct {
	Name   string
	Params []Param
	Value  string
}

// ParseProperty parses one unfolded property line.
//
// ../rfc/5545:3.2
func ParseProperty(line []byte) (Property, error) {
	colon := indexUnquoted(line, ':')
	if colon < 0 {
		return Property{}, fmt.Errorf("icalendar: property without value")
	}

	parts := splitUnquoted(line[:colon], ';')
	if len(parts) == 0 || len(parts[0]) == 0 {
		return Property{}, fmt.Errorf("icalendar: empty property name")
	}

	prop := Property{
		Name: strings.ToUpper(string(parts[0])),
	}
	for _, part := range parts[1:] {
		if len(part) == 0 {
			continue
		}
		eq := indexUnquoted(part, '=')
		if eq < 0 {
			return Property{}, fmt.Errorf("icalendar: param without value")
		}
		name := strings.ToUpper(string(part[:eq]))
		value, err := parseParamValue(part[eq+1:])
		if err != nil {
			return Property{}, err
		}
		prop.Params = append(prop.Params, Param{Name: name, Values: []string{value}})
	}

	value := bytes.TrimRight(line[colon+1:], "\r\n")
	prop.Value = UnescapeText(string(value))
	return prop, nil
}

// Param returns the first value of the named parameter, case-insensitively.
func (p Property) Param(name string) (string, bool) {
	for _, param := range p.Params {
		if strings.EqualFold(param.Name, name) {
			if len(param.Values) == 0 {
				return "", true
			}
			return param.Values[0], true
		}
	}
	return "", false
}

// WriteTo writes one property, folding at 75 octets.
//
// ../rfc/5545:3.1
func (p Property) WriteTo(w io.Writer) error {
	var line []byte
	line = append(line, p.Name...)
	for _, param := range p.Params {
		line = append(line, ';')
		line = append(line, param.Name...)
		line = append(line, '=')
		line = append(line, formatParamValue(param)...)
	}
	line = append(line, ':')
	value := p.Value
	if p.textValue() {
		value = EscapeText(value)
	}
	line = append(line, value...)
	return writeFolded(w, line)
}

// EscapeText escapes an iCalendar TEXT value.
//
// ../rfc/5545:3.3.11
func EscapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case ';':
			b.WriteString(`\;`)
		case ',':
			b.WriteString(`\,`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// UnescapeText unescapes an iCalendar TEXT value.
//
// ../rfc/5545:3.3.11
func UnescapeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '\\':
			b.WriteByte('\\')
		case ';':
			b.WriteByte(';')
		case ',':
			b.WriteByte(',')
		case 'n', 'N':
			b.WriteByte('\n')
		default:
			b.WriteByte('\\')
			continue
		}
		i++
	}
	return b.String()
}

func (p Property) textValue() bool {
	if v, ok := p.Param("VALUE"); ok {
		return strings.EqualFold(v, "TEXT")
	}
	switch p.Name {
	case "ATTACH", "ATTENDEE", "COMPLETED", "CREATED", "DTEND", "DTSTAMP", "DTSTART", "DUE",
		"EXDATE", "FREEBUSY", "GEO", "LAST-MODIFIED", "ORGANIZER", "PERCENT-COMPLETE", "PRIORITY",
		"RDATE", "RECURRENCE-ID", "SEQUENCE", "TRIGGER", "TZOFFSETFROM", "TZOFFSETTO", "URL":
		return false
	default:
		return true
	}
}

func parseParamValue(b []byte) (string, error) {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		return unescapeQuotedParam(string(b[1 : len(b)-1])), nil
	}
	return string(b), nil
}

func unescapeQuotedParam(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func formatParamValue(param Param) string {
	if len(param.Values) == 0 {
		return `""`
	}
	v := param.Values[0]
	if paramNeedsQuote(v) {
		return `"` + escapeQuotedParam(v) + `"`
	}
	return v
}

func paramNeedsQuote(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ';', ',', ':', '"', '\n', '\r':
			return true
		}
	}
	return false
}

func escapeQuotedParam(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '"' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func writeFolded(w io.Writer, line []byte) error {
	for start := 0; start < len(line); {
		end := start + 75
		if end > len(line) {
			end = len(line)
		}
		if _, err := w.Write(line[start:end]); err != nil {
			return err
		}
		start = end
		if start < len(line) {
			if _, err := w.Write([]byte("\r\n ")); err != nil {
				return err
			}
		}
	}
	_, err := w.Write([]byte("\r\n"))
	return err
}

func indexUnquoted(b []byte, sep byte) int {
	inQuote := false
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			inQuote = !inQuote
		case '\\':
			if inQuote && i+1 < len(b) {
				i++
			}
		default:
			if !inQuote && b[i] == sep {
				return i
			}
		}
	}
	return -1
}

func splitUnquoted(b []byte, sep byte) [][]byte {
	var parts [][]byte
	start := 0
	inQuote := false
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			inQuote = !inQuote
		case '\\':
			if inQuote && i+1 < len(b) {
				i++
			}
		default:
			if !inQuote && b[i] == sep {
				parts = append(parts, b[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, b[start:])
	return parts
}
