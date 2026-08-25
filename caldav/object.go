package caldav

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/mjl-/mox/calendar"
)

func objectIfMatch(r *http.Request) string {
	if r.Header.Get("If-None-Match") == "*" {
		return "*"
	}
	return r.Header.Get("If-Match")
}

func writeObjectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, calendar.ErrNotFound), errors.Is(err, calendar.ErrNotEnabled):
		http.NotFound(w, r)
	case errors.Is(err, calendar.ErrStale):
		http.Error(w, "412 - precondition failed", http.StatusPreconditionFailed)
	case errors.Is(err, calendar.ErrLimit):
		http.Error(w, "413 - payload too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, calendar.ErrInvalid):
		if r.Method == http.MethodPut {
			http.Error(w, "409 - conflict", http.StatusConflict)
			return
		}
		http.Error(w, "403 - forbidden", http.StatusForbidden)
	default:
		pkglog.WithContext(r.Context()).Errorx("calendar object", err)
		http.Error(w, "500 - internal server error", http.StatusInternalServerError)
	}
}

// ../rfc/4791:5.3.2
func (h handler) serveObjectGet(w http.ResponseWriter, r *http.Request, res resource) {
	acc := accountFrom(r)
	obj, err := calendar.Service{}.GetObject(r.Context(), acc, res.uid)
	if err != nil {
		writeObjectError(w, r, err)
		return
	}
	w.Header().Set("ETag", obj.ETag)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.ICS)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write(obj.ICS)
}

// ../rfc/4791:5.3.4
func (h handler) servePut(w http.ResponseWriter, r *http.Request, res resource) {
	if res.kind != resObject {
		http.NotFound(w, r)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/calendar") {
		http.Error(w, "415 - unsupported media type", http.StatusUnsupportedMediaType)
		return
	}

	body := http.MaxBytesReader(w, r.Body, int64(calendar.Limits().MaxICSBytes))
	ics, err := io.ReadAll(body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "413 - payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "400 - bad request", http.StatusBadRequest)
		return
	}

	acc := accountFrom(r)
	svc := calendar.Service{}
	existed := true
	_, err = svc.GetObject(r.Context(), acc, res.uid)
	if errors.Is(err, calendar.ErrNotFound) {
		existed = false
	} else if err != nil {
		writeObjectError(w, r, err)
		return
	}

	obj, err := svc.PutObject(r.Context(), acc, res.uid, ics, objectIfMatch(r))
	if err != nil {
		writeObjectError(w, r, err)
		return
	}
	w.Header().Set("ETag", obj.ETag)
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// ../rfc/4918:9.6
func (h handler) serveDelete(w http.ResponseWriter, r *http.Request, res resource) {
	if res.kind != resObject {
		http.NotFound(w, r)
		return
	}
	acc := accountFrom(r)
	err := calendar.Service{}.DeleteObject(r.Context(), acc, res.uid, objectIfMatch(r))
	if err != nil {
		writeObjectError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
