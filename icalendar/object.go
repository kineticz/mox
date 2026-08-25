package icalendar

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Component is one iCalendar component such as VEVENT or VALARM.
type Component struct {
	Name       string
	Properties []Property
	Components []Component
}

// Calendar is a parsed VCALENDAR object.
type Calendar struct {
	Properties []Property
	Components []Component
}

// Parse parses iCalendar data into a Calendar.
func Parse(data []byte) (Calendar, error) {
	if err := rejectTrailingAfterCalendarEnd(data); err != nil {
		return Calendar{}, err
	}
	unfolded, err := Unfold(data)
	if err != nil {
		return Calendar{}, err
	}
	lines, err := splitContentLines(unfolded)
	if err != nil {
		return Calendar{}, err
	}

	var p parser
	for _, line := range lines {
		if err := p.line(line); err != nil {
			return Calendar{}, err
		}
	}
	if err := p.finish(); err != nil {
		return Calendar{}, err
	}
	return p.cal, nil
}

func rejectTrailingAfterCalendarEnd(raw []byte) error {
	lines, err := splitContentLines(raw)
	if err != nil {
		return err
	}
	var stack int
	var inCal bool
	var calClosed bool
	for _, line := range lines {
		if calClosed {
			return fmt.Errorf("icalendar: data after END:VCALENDAR")
		}
		trimmed := bytes.TrimRight(line, "\r")
		if len(bytes.TrimSpace(trimmed)) == 0 {
			continue
		}
		if isBeginLine(trimmed) {
			name, err := componentName(trimmed, "BEGIN")
			if err != nil {
				continue
			}
			if name == "VCALENDAR" {
				inCal = true
				continue
			}
			if inCal {
				stack++
			}
			continue
		}
		if isEndLine(trimmed) {
			name, err := componentName(trimmed, "END")
			if err != nil {
				continue
			}
			if name == "VCALENDAR" {
				if !inCal || stack != 0 {
					continue
				}
				calClosed = true
				continue
			}
			if stack > 0 {
				stack--
			}
		}
	}
	return nil
}

// Serialize returns the iCalendar representation of c.
func (c Calendar) Serialize() []byte {
	var buf bytes.Buffer
	writeComponentBlock(&buf, "VCALENDAR", c.Properties, c.Components)
	return buf.Bytes()
}

// Serialize returns the iCalendar representation of this component, including nested components.
func (c Component) Serialize() []byte {
	var buf bytes.Buffer
	c.writeTo(&buf)
	return buf.Bytes()
}

// Prop returns the first property with the given name.
func (c Calendar) Prop(name string) (Property, bool) {
	name = strings.ToUpper(name)
	for _, p := range c.Properties {
		if p.Name == name {
			return p, true
		}
	}
	return Property{}, false
}

// Method returns the first METHOD property value, uppercased, or "" if absent.
func (c Calendar) Method() string {
	p, ok := c.Prop("METHOD")
	if !ok {
		return ""
	}
	return strings.ToUpper(p.Value)
}

// Prop returns the first property with the given name.
func (c Component) Prop(name string) (Property, bool) {
	name = strings.ToUpper(name)
	for _, p := range c.Properties {
		if p.Name == name {
			return p, true
		}
	}
	return Property{}, false
}

// UID returns the component UID property value, or "" if absent.
func (c Component) UID() string {
	p, ok := c.Prop("UID")
	if !ok {
		return ""
	}
	return p.Value
}

// RecurrenceID returns the RECURRENCE-ID property as a DateTime.
func (c Component) RecurrenceID() (DateTime, bool, error) {
	p, ok := c.Prop("RECURRENCE-ID")
	if !ok {
		return DateTime{}, false, nil
	}
	d, err := ParseDateTime(p)
	return d, true, err
}

// Sequence returns the SEQUENCE property, or 0 if absent.
func (c Component) Sequence() int {
	p, ok := c.Prop("SEQUENCE")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(p.Value)
	if err != nil {
		return 0
	}
	return n
}

// Props returns all properties with the given name.
func (c Component) Props(name string) []Property {
	name = strings.ToUpper(name)
	var out []Property
	for _, p := range c.Properties {
		if p.Name == name {
			out = append(out, p)
		}
	}
	return out
}

type parser struct {
	cal   Calendar
	stack []Component
	inCal bool
	done  bool
}

func (p *parser) finish() error {
	if !p.inCal {
		return fmt.Errorf("icalendar: missing BEGIN:VCALENDAR")
	}
	if !p.done {
		return fmt.Errorf("icalendar: missing END:VCALENDAR")
	}
	if len(p.stack) != 0 {
		return fmt.Errorf("icalendar: mismatched BEGIN/END")
	}
	return nil
}

func (p *parser) line(line []byte) error {
	if p.done {
		return fmt.Errorf("icalendar: data after END:VCALENDAR")
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}

	if isBeginLine(line) {
		name, err := componentName(line, "BEGIN")
		if err != nil {
			return err
		}
		return p.begin(name)
	}
	if isEndLine(line) {
		name, err := componentName(line, "END")
		if err != nil {
			return err
		}
		return p.end(name)
	}

	prop, err := ParseProperty(line)
	if err != nil {
		return err
	}
	if !p.inCal {
		return fmt.Errorf("icalendar: missing BEGIN:VCALENDAR")
	}
	if len(p.stack) == 0 {
		p.cal.Properties = append(p.cal.Properties, prop)
	} else {
		top := &p.stack[len(p.stack)-1]
		top.Properties = append(top.Properties, prop)
	}
	return nil
}

func (p *parser) begin(name string) error {
	if name == "VCALENDAR" {
		if p.inCal {
			return fmt.Errorf("icalendar: nested VCALENDAR")
		}
		p.inCal = true
		return nil
	}
	if !p.inCal {
		return fmt.Errorf("icalendar: missing BEGIN:VCALENDAR")
	}
	p.stack = append(p.stack, Component{Name: name})
	return nil
}

func (p *parser) end(name string) error {
	if name == "VCALENDAR" {
		if !p.inCal {
			return fmt.Errorf("icalendar: missing BEGIN:VCALENDAR")
		}
		if len(p.stack) != 0 {
			return fmt.Errorf("icalendar: mismatched BEGIN/END")
		}
		p.done = true
		return nil
	}
	if len(p.stack) == 0 {
		return fmt.Errorf("icalendar: mismatched BEGIN/END")
	}
	comp := p.stack[len(p.stack)-1]
	if comp.Name != name {
		return fmt.Errorf("icalendar: mismatched BEGIN/END")
	}
	p.stack = p.stack[:len(p.stack)-1]
	if len(p.stack) == 0 {
		p.cal.Components = append(p.cal.Components, comp)
	} else {
		parent := &p.stack[len(p.stack)-1]
		parent.Components = append(parent.Components, comp)
	}
	return nil
}

func splitContentLines(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var lines [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			lines = append(lines, bytes.TrimRight(data, "\r"))
			break
		}
		line := bytes.TrimRight(data[:i], "\r")
		data = data[i+1:]
		lines = append(lines, line)
	}
	return lines, nil
}

func isBeginLine(line []byte) bool {
	return hasPrefixFold(line, "BEGIN:")
}

func isEndLine(line []byte) bool {
	return hasPrefixFold(line, "END:")
}

func hasPrefixFold(line []byte, prefix string) bool {
	if len(line) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := line[i], prefix[i]
		if a >= 'a' && a <= 'z' {
			a -= 'a' - 'A'
		}
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func componentName(line []byte, keyword string) (string, error) {
	prefix := keyword + ":"
	if !hasPrefixFold(line, prefix) {
		return "", fmt.Errorf("icalendar: invalid component line")
	}
	name := strings.ToUpper(string(line[len(prefix):]))
	if name == "" {
		return "", fmt.Errorf("icalendar: empty component name")
	}
	return name, nil
}

func writeComponentBlock(w io.Writer, name string, props []Property, comps []Component) {
	fmt.Fprintf(w, "BEGIN:%s\r\n", name)
	for _, p := range props {
		_ = p.WriteTo(w)
	}
	for _, c := range comps {
		c.writeTo(w)
	}
	fmt.Fprintf(w, "END:%s\r\n", name)
}

func (c Component) writeTo(w io.Writer) {
	writeComponentBlock(w, c.Name, c.Properties, c.Components)
}
