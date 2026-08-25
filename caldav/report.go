package caldav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mjl-/mox/calendar"
	"github.com/mjl-/mox/store"
)

var errUnsupportedReport = errors.New("unsupported report")

type reportKind int

const (
	reportUnknown reportKind = iota
	reportQuery
	reportMultiget
)

type parsedReport struct {
	kind     reportKind
	hasRange bool
	start    string
	end      string
	hrefs    []string
}

// ../rfc/4791:7.8
// ../rfc/4791:7.9
func (h handler) serveReport(w http.ResponseWriter, r *http.Request, acc *store.Account, res resource) {
	if res.kind != resCalendar {
		http.NotFound(w, r)
		return
	}

	rep, err := parseReport(r.Body)
	if err != nil {
		if errors.Is(err, errUnsupportedReport) {
			http.Error(w, "403 - forbidden - unsupported report", http.StatusForbidden)
			return
		}
		http.Error(w, "400 - bad request - invalid xml", http.StatusBadRequest)
		return
	}

	switch rep.kind {
	case reportQuery:
		h.reportQuery(w, r, acc, res, rep)
	case reportMultiget:
		h.reportMultiget(w, r, acc, res, rep)
	default:
		http.Error(w, "403 - forbidden - unsupported report", http.StatusForbidden)
	}
}

func (h handler) reportQuery(w http.ResponseWriter, r *http.Request, acc *store.Account, res resource, rep parsedReport) {
	svc := calendar.Service{}
	var objs []calendar.CalendarObject
	if !rep.hasRange {
		var err error
		objs, err = svc.ListObjects(r.Context(), acc)
		if err != nil {
			writeObjectError(w, r, err)
			return
		}
	} else {
		if rep.start == "" && rep.end == "" {
			http.Error(w, "400 - bad request - invalid time-range", http.StatusBadRequest)
			return
		}
		horizonDays := calendar.Limits().ExpansionHorizonDays
		var from time.Time
		to := time.Now().UTC().AddDate(0, 0, horizonDays)
		if rep.start != "" {
			t, err := parseReportTime(rep.start)
			if err != nil {
				http.Error(w, "400 - bad request - invalid time-range", http.StatusBadRequest)
				return
			}
			from = t
		}
		if rep.end != "" {
			t, err := parseReportTime(rep.end)
			if err != nil {
				http.Error(w, "400 - bad request - invalid time-range", http.StatusBadRequest)
				return
			}
			to = t
		}
		// Keep the recent end of the window. Service.Query clamps `to` from `from`,
		// so a 1970 start (or a multi-year client range) would drop current events.
		if rep.start == "" || to.After(from.AddDate(0, 0, horizonDays)) {
			from = to.AddDate(0, 0, -horizonDays)
		}
		insts, err := svc.Query(r.Context(), acc, from, to)
		if err != nil {
			writeObjectError(w, r, err)
			return
		}
		seen := map[string]bool{}
		for _, inst := range insts {
			uid := inst.Event.UID
			if seen[uid] {
				continue
			}
			seen[uid] = true
			obj, err := svc.GetObject(r.Context(), acc, uid)
			if err != nil {
				writeObjectError(w, r, err)
				return
			}
			objs = append(objs, obj)
		}
	}

	responses := make([]xmlResponse, 0, len(objs))
	for _, obj := range objs {
		responses = append(responses, objectDataResponse(res.account, obj))
	}
	writeMultistatus(w, responses)
}

func (h handler) reportMultiget(w http.ResponseWriter, r *http.Request, acc *store.Account, _ resource, rep parsedReport) {
	svc := calendar.Service{}
	responses := make([]xmlResponse, 0, len(rep.hrefs))
	for _, href := range rep.hrefs {
		objRes := resourceFromHref(href)
		if objRes.kind != resObject || objRes.account != acc.Name {
			responses = append(responses, objectMissingResponse(href))
			continue
		}
		obj, err := svc.GetObject(r.Context(), acc, objRes.uid)
		if errors.Is(err, calendar.ErrNotFound) {
			responses = append(responses, objectMissingResponse(href))
			continue
		}
		if err != nil {
			writeObjectError(w, r, err)
			return
		}
		resp := objectDataResponse(acc.Name, obj)
		if href != "" {
			resp.Href = href
		}
		responses = append(responses, resp)
	}
	writeMultistatus(w, responses)
}

func objectDataResponse(account string, obj calendar.CalendarObject) xmlResponse {
	res := resource{kind: resObject, account: account, uid: obj.UID}
	inner := encodeProp(xml.Name{Space: nsDAV, Local: "getetag"}, xmlText(obj.ETag)) +
		encodeProp(xml.Name{Space: nsCalDAV, Local: "calendar-data"}, xmlText(string(obj.ICS)))
	return xmlResponse{
		Href: res.href(),
		Propstat: []xmlPropstat{{
			Prop:   xmlProp{Inner: inner},
			Status: "HTTP/1.1 200 OK",
		}},
	}
}

func objectMissingResponse(href string) xmlResponse {
	inner := encodeProp(xml.Name{Space: nsCalDAV, Local: "calendar-data"}, "")
	return xmlResponse{
		Href: href,
		Propstat: []xmlPropstat{{
			Prop:   xmlProp{Inner: inner},
			Status: "HTTP/1.1 404 Not Found",
		}},
	}
}

func parseReport(r io.Reader) (parsedReport, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return parsedReport{}, err
	}

	var p parsedReport
	dec := xml.NewDecoder(bytes.NewReader(data))
	var href strings.Builder
	inHref := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return parsedReport{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsCalDAV && t.Name.Local == "calendar-query" && p.kind == reportUnknown {
				p.kind = reportQuery
			}
			if t.Name.Space == nsCalDAV && t.Name.Local == "calendar-multiget" && p.kind == reportUnknown {
				p.kind = reportMultiget
			}
			if t.Name.Space == nsCalDAV && t.Name.Local == "time-range" {
				p.hasRange = true
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "start":
						p.start = a.Value
					case "end":
						p.end = a.Value
					}
				}
			}
			if t.Name.Space == nsDAV && t.Name.Local == "href" {
				inHref = true
				href.Reset()
			}
		case xml.EndElement:
			if inHref && t.Name.Space == nsDAV && t.Name.Local == "href" {
				inHref = false
				if h := strings.TrimSpace(href.String()); h != "" {
					p.hrefs = append(p.hrefs, h)
				}
			}
		case xml.CharData:
			if inHref {
				href.Write([]byte(t))
			}
		}
	}
	if p.kind == reportUnknown {
		return parsedReport{}, errUnsupportedReport
	}
	return p, nil
}

func parseReportTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	// ../rfc/4791:9.9 CalDAV time-range uses iCalendar UTC date-time.
	if t, err := time.Parse("20060102T150405Z", s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("invalid time-range")
}

func resourceFromHref(href string) resource {
	u, err := url.Parse(href)
	if err != nil {
		return resource{kind: resNone}
	}
	path := u.Path
	if after, ok := strings.CutPrefix(path, "/caldav"); ok {
		path = after
	}
	if path == "" {
		path = "/"
	}
	return parseResource(path)
}
