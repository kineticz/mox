package caldav

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
)

const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsCS     = "http://calendarserver.org/ns/"
)

// Compliance-set properties for empty-body / allprop PROPFIND.
// ../rfc/4918:9.1
var allPropNames = []xml.Name{
	{Space: nsDAV, Local: "resourcetype"},
	{Space: nsDAV, Local: "displayname"},
	{Space: nsDAV, Local: "getetag"},
	{Space: nsDAV, Local: "getcontenttype"},
	{Space: nsDAV, Local: "current-user-principal"},
	{Space: nsDAV, Local: "principal-URL"},
	{Space: nsCalDAV, Local: "calendar-home-set"},
	{Space: nsCalDAV, Local: "supported-calendar-component-set"},
	{Space: nsCS, Local: "getctag"},
}

type xmlMultistatus struct {
	XMLName  xml.Name      `xml:"D:multistatus"`
	XmlnsD   string        `xml:"xmlns:D,attr"`
	XmlnsC   string        `xml:"xmlns:C,attr"`
	XmlnsCS  string        `xml:"xmlns:CS,attr"`
	Response []xmlResponse `xml:"D:response"`
}

type xmlResponse struct {
	Href     string        `xml:"D:href"`
	Propstat []xmlPropstat `xml:"D:propstat"`
}

type xmlPropstat struct {
	Prop   xmlProp `xml:"D:prop"`
	Status string  `xml:"D:status"`
}

type xmlProp struct {
	Inner string `xml:",innerxml"`
}

func writeMultistatus(w http.ResponseWriter, responses []xmlResponse) {
	ms := xmlMultistatus{
		XmlnsD:   nsDAV,
		XmlnsC:   nsCalDAV,
		XmlnsCS:  nsCS,
		Response: responses,
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(ms); err != nil {
		http.Error(w, "500 - internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	w.Write(buf.Bytes())
}

func parsePropfind(r io.Reader) (names []xml.Name, allprop bool, err error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, false, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, true, nil
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	var inProp bool
	propDepth := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsDAV && (t.Name.Local == "allprop" || t.Name.Local == "propname") {
				allprop = true
				continue
			}
			if t.Name.Space == nsDAV && t.Name.Local == "prop" && !inProp {
				inProp = true
				propDepth = 0
				continue
			}
			if inProp {
				if propDepth == 0 {
					names = append(names, t.Name)
				}
				propDepth++
			}
		case xml.EndElement:
			if !inProp {
				continue
			}
			if t.Name.Space == nsDAV && t.Name.Local == "prop" && propDepth == 0 {
				inProp = false
				continue
			}
			if propDepth > 0 {
				propDepth--
			}
		}
	}
	return names, allprop, nil
}

func encodeProp(name xml.Name, inner string) string {
	prefix, extra := nsPrefix(name)
	var b strings.Builder
	if inner == "" {
		b.WriteByte('<')
		if prefix != "" {
			b.WriteString(prefix)
			b.WriteByte(':')
		}
		b.WriteString(name.Local)
		if extra != "" {
			b.WriteString(` xmlns="`)
			escapeXML(&b, extra)
			b.WriteByte('"')
		}
		b.WriteString("/>")
		return b.String()
	}
	b.WriteByte('<')
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteByte(':')
	}
	b.WriteString(name.Local)
	if extra != "" {
		b.WriteString(` xmlns="`)
		escapeXML(&b, extra)
		b.WriteByte('"')
	}
	b.WriteByte('>')
	b.WriteString(inner)
	b.WriteString("</")
	if prefix != "" {
		b.WriteString(prefix)
		b.WriteByte(':')
	}
	b.WriteString(name.Local)
	b.WriteByte('>')
	return b.String()
}

func nsPrefix(name xml.Name) (prefix, extraNS string) {
	switch name.Space {
	case nsDAV:
		return "D", ""
	case nsCalDAV:
		return "C", ""
	case nsCS:
		return "CS", ""
	case "":
		return "", ""
	default:
		return "", name.Space
	}
}

func hrefInner(path string) string {
	var b strings.Builder
	b.WriteString("<D:href>")
	escapeXML(&b, path)
	b.WriteString("</D:href>")
	return b.String()
}

func escapeXML(b *strings.Builder, s string) {
	xml.EscapeText(b, []byte(s))
}

func xmlText(s string) string {
	var b strings.Builder
	escapeXML(&b, s)
	return b.String()
}
