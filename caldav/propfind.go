package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mjl-/bstore"

	"github.com/mjl-/mox/calendar"
	"github.com/mjl-/mox/store"
)

type resourceKind int

const (
	resNone resourceKind = iota
	resRoot
	resPrincipal
	resCalendarHome
	resCalendar
	resObject
)

type resource struct {
	kind    resourceKind
	account string
	uid     string
	obj     *calendar.CalendarObject
}

func parseResource(path string) resource {
	if path == "" || path == "/" {
		return resource{kind: resRoot}
	}
	path = strings.TrimPrefix(path, "/")
	account, rest, found := strings.Cut(path, "/")
	if account == "" {
		return resource{kind: resRoot}
	}
	if !found || rest == "" || rest == "/" {
		return resource{kind: resPrincipal, account: account}
	}
	rest = strings.TrimSuffix(rest, "/")
	switch rest {
	case "calendars":
		return resource{kind: resCalendarHome, account: account}
	case "calendars/default":
		return resource{kind: resCalendar, account: account}
	}
	const objPrefix = "calendars/default/"
	if !strings.HasPrefix(rest, objPrefix) {
		return resource{kind: resNone, account: account}
	}
	file := rest[len(objPrefix):]
	if !strings.HasSuffix(file, ".ics") {
		return resource{kind: resNone, account: account}
	}
	raw := strings.TrimSuffix(file, ".ics")
	if raw == "" {
		return resource{kind: resNone, account: account}
	}
	uid, err := url.PathUnescape(raw)
	if err != nil || uid == "" {
		return resource{kind: resNone, account: account}
	}
	return resource{kind: resObject, account: account, uid: uid}
}

func (res resource) href() string {
	switch res.kind {
	case resPrincipal:
		return "/caldav/" + res.account + "/"
	case resCalendarHome:
		return "/caldav/" + res.account + "/calendars/"
	case resCalendar:
		return "/caldav/" + res.account + "/calendars/default/"
	case resObject:
		return "/caldav/" + res.account + "/calendars/default/" + url.PathEscape(res.uid) + ".ics"
	default:
		return "/caldav/"
	}
}

func parseDepth(h string, kind resourceKind) (int, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, nil
	}
	switch strings.ToLower(h) {
	case "0":
		return 0, nil
	case "1":
		return 1, nil
	case "infinity":
		// ../rfc/4918:9.1 Depth infinity on a calendar collection is Depth 1.
		if kind == resCalendar || kind == resCalendarHome || kind == resPrincipal || kind == resRoot {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid depth")
	}
}

// ../rfc/4918:9.1
// ../rfc/4791:5
func (h handler) propfind(w http.ResponseWriter, r *http.Request, acc *store.Account, res resource) {
	log := pkglog.WithContext(r.Context())

	if res.kind == resNone || res.kind == resObject {
		http.NotFound(w, r)
		return
	}

	depth, err := parseDepth(r.Header.Get("Depth"), res.kind)
	if err != nil {
		http.Error(w, "400 - bad request - invalid Depth", http.StatusBadRequest)
		return
	}

	names, allprop, err := parsePropfind(r.Body)
	if err != nil {
		http.Error(w, "400 - bad request - invalid xml", http.StatusBadRequest)
		return
	}
	if allprop {
		names = allPropNames
	}

	cal, err := defaultCalendar(r.Context(), acc)
	if err != nil {
		log.Errorx("load default calendar", err)
		http.Error(w, "500 - internal server error", http.StatusInternalServerError)
		return
	}

	targets := []resource{res}
	if depth >= 1 {
		children, err := listChildren(r.Context(), acc, res)
		if err != nil {
			log.Errorx("list propfind children", err)
			http.Error(w, "500 - internal server error", http.StatusInternalServerError)
			return
		}
		targets = append(targets, children...)
	}

	responses := make([]xmlResponse, 0, len(targets))
	for _, t := range targets {
		responses = append(responses, propfindResource(acc, t, cal, names))
	}
	writeMultistatus(w, responses)
}

func defaultCalendar(ctx context.Context, acc *store.Account) (store.Calendar, error) {
	var cal store.Calendar
	err := acc.DB.Read(ctx, func(tx *bstore.Tx) error {
		var err error
		cal, err = acc.DefaultCalendar(tx)
		return err
	})
	return cal, err
}

func listChildren(ctx context.Context, acc *store.Account, res resource) ([]resource, error) {
	switch res.kind {
	case resRoot:
		return []resource{{kind: resPrincipal, account: acc.Name}}, nil
	case resPrincipal:
		return []resource{{kind: resCalendarHome, account: acc.Name}}, nil
	case resCalendarHome:
		return []resource{{kind: resCalendar, account: acc.Name}}, nil
	case resCalendar:
		objs, err := calendar.Service{}.ListObjects(ctx, acc)
		if err != nil {
			return nil, err
		}
		out := make([]resource, 0, len(objs))
		for i := range objs {
			out = append(out, resource{kind: resObject, account: acc.Name, uid: objs[i].UID, obj: &objs[i]})
		}
		return out, nil
	}
	return nil, nil
}

func propfindResource(acc *store.Account, res resource, cal store.Calendar, names []xml.Name) xmlResponse {
	var okInner, notInner strings.Builder
	for _, name := range names {
		inner, ok := liveProp(acc, res, cal, name)
		enc := encodeProp(name, inner)
		if ok {
			okInner.WriteString(enc)
		} else {
			notInner.WriteString(enc)
		}
	}
	resp := xmlResponse{Href: res.href()}
	if okInner.Len() > 0 {
		resp.Propstat = append(resp.Propstat, xmlPropstat{
			Prop:   xmlProp{Inner: okInner.String()},
			Status: "HTTP/1.1 200 OK",
		})
	}
	if notInner.Len() > 0 {
		resp.Propstat = append(resp.Propstat, xmlPropstat{
			Prop:   xmlProp{Inner: notInner.String()},
			Status: "HTTP/1.1 404 Not Found",
		})
	}
	return resp
}

func liveProp(acc *store.Account, res resource, cal store.Calendar, name xml.Name) (inner string, ok bool) {
	switch name.Space {
	case nsDAV:
		switch name.Local {
		case "resourcetype":
			return resourceTypeInner(res.kind), true
		case "displayname":
			if res.kind == resCalendar {
				return xmlText(cal.Name), true
			}
		case "getetag":
			if res.kind == resCalendar {
				return xmlText(ctagValue(cal.CTag)), true
			}
			if res.kind == resObject && res.obj != nil {
				return xmlText(res.obj.ETag), true
			}
		case "getcontenttype":
			if res.kind == resObject {
				return xmlText("text/calendar; charset=utf-8"), true
			}
		case "current-user-principal":
			return hrefInner("/caldav/" + acc.Name + "/"), true
		case "principal-URL":
			if res.kind == resPrincipal {
				return hrefInner("/caldav/" + acc.Name + "/"), true
			}
		}
	case nsCalDAV:
		switch name.Local {
		case "calendar-home-set":
			// ../rfc/4791:6.2.1
			if res.kind == resPrincipal {
				return hrefInner("/caldav/" + acc.Name + "/calendars/"), true
			}
		case "supported-calendar-component-set":
			// ../rfc/4791:5.3.3
			if res.kind == resCalendar {
				return `<C:comp name="VEVENT"/>`, true
			}
		}
	case nsCS:
		if name.Local == "getctag" && res.kind == resCalendar {
			return xmlText(ctagValue(cal.CTag)), true
		}
	}
	return "", false
}

func resourceTypeInner(kind resourceKind) string {
	switch kind {
	case resRoot, resCalendarHome:
		return `<D:collection/>`
	case resPrincipal:
		return `<D:principal/>`
	case resCalendar:
		// ../rfc/4791:4.2
		return `<D:collection/><C:calendar/>`
	default:
		return ""
	}
}

func ctagValue(n int64) string {
	return fmt.Sprintf(`"ctag-%d"`, n)
}
