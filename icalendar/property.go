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
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return Property{}, fmt.Errorf("icalendar: empty property")
	}

	i := 0
	nameStart := i
	for i < len(line) && line[i] != ';' && line[i] != ':' {
		i++
	}
	if nameStart == i {
		return Property{}, fmt.Errorf("icalendar: empty property name")
	}
	name := strings.ToUpper(string(line[nameStart:i]))
	if !validName(name) {
		return Property{}, fmt.Errorf("icalendar: invalid property name")
	}
	prop := Property{Name: name}

	for i < len(line) && line[i] == ';' {
		i++
		if i >= len(line) {
			return Property{}, fmt.Errorf("icalendar: empty param")
		}
		paramStart := i
		for i < len(line) && line[i] != '=' {
			if line[i] == ';' || line[i] == ':' {
				return Property{}, fmt.Errorf("icalendar: param without value")
			}
			i++
		}
		if i >= len(line) || paramStart == i {
			return Property{}, fmt.Errorf("icalendar: empty param name")
		}
		pname := strings.ToUpper(string(line[paramStart:i]))
		if !validName(pname) {
			return Property{}, fmt.Errorf("icalendar: invalid param name")
		}
		i++
		valueStart := i
		inQuote := false
		for i < len(line) {
			if line[i] == '"' {
				inQuote = !inQuote
				i++
				continue
			}
			if !inQuote && (line[i] == ';' || line[i] == ':') {
				break
			}
			i++
		}
		values := parseParamValue(line[valueStart:i])
		prop.Params = append(prop.Params, Param{Name: pname, Values: values})
	}

	if i >= len(line) || line[i] != ':' {
		return Property{}, fmt.Errorf("icalendar: property without value")
	}
	rawValue := string(line[i+1:])
	if prop.textValue() {
		prop.Value = UnescapeText(rawValue)
	} else {
		prop.Value = rawValue
	}
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

func validName(name string) bool {
	if name == "" {
		return false
	}
	rest := name
	if strings.HasPrefix(name, "X-") {
		rest = name[2:]
		if rest == "" {
			return false
		}
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func parseParamValue(b []byte) []string {
	return splitParamValuesBytes(b)
}

func splitParamValuesBytes(b []byte) []string {
	if len(b) == 0 {
		return []string{""}
	}
	var values []string
	for len(b) > 0 {
		if b[0] == '"' {
			end := 1
			for end < len(b) {
				if b[end] == '"' {
					values = append(values, string(b[1:end]))
					end++
					break
				}
				end++
			}
			b = b[end:]
			if len(b) > 0 && b[0] == ',' {
				b = b[1:]
			}
			continue
		}
		i := 0
		for i < len(b) && b[i] != ',' {
			i++
		}
		values = append(values, string(b[:i]))
		if i < len(b) {
			b = b[i+1:]
		} else {
			break
		}
	}
	return values
}

func formatParamValue(param Param) string {
	if len(param.Values) == 0 {
		return `""`
	}
	parts := make([]string, len(param.Values))
	for i, v := range param.Values {
		if paramNeedsQuote(v) {
			parts[i] = `"` + v + `"`
		} else {
			parts[i] = v
		}
	}
	return strings.Join(parts, ",")
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

func writeFolded(w io.Writer, line []byte) error {
	first := true
	for start := 0; start < len(line); {
		limit := 75
		if !first {
			limit = 74
		}
		end := start + limit
		if end > len(line) {
			end = len(line)
		}
		if !first {
			if _, err := w.Write([]byte("\r\n ")); err != nil {
				return err
			}
		}
		if _, err := w.Write(line[start:end]); err != nil {
			return err
		}
		start = end
		first = false
	}
	_, err := w.Write([]byte("\r\n"))
	return err
}
