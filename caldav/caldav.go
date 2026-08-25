// Package caldav implements a CalDAV/WebDAV HTTP adapter for calendar storage.
package caldav

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mjl-/mox/metrics"
	"github.com/mjl-/mox/mlog"
	"github.com/mjl-/mox/mox-"
	"github.com/mjl-/mox/store"
	"github.com/mjl-/mox/webauth"
)

var pkglog = mlog.New("caldav", nil)

type ctxKey int

const accountCtxKey ctxKey = 1

func accountFrom(r *http.Request) *store.Account {
	acc, _ := r.Context().Value(accountCtxKey).(*store.Account)
	return acc
}

type handler struct {
	isForwarded bool
}

// Handler returns the CalDAV HTTP handler. It is mounted at /caldav with the
// prefix stripped, so r.URL.Path is "", "/", "/{account}", etc.
func Handler(isForwarded bool) http.Handler {
	return handler{isForwarded: isForwarded}
}

// WellKnown redirects GET/HEAD /.well-known/caldav to /caldav/.
func WellKnown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !mox.CalDAVEnabled() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Location", "/caldav/")
	w.WriteHeader(http.StatusMovedPermanently)
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := pkglog.WithContext(r.Context())

	if !mox.CalDAVEnabled() {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodOptions {
		w.Header().Set("DAV", "1, 3, calendar-access")
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, REPORT")
		w.WriteHeader(http.StatusOK)
		return
	}

	acc, ok := h.authenticate(log, w, r)
	if !ok {
		return
	}
	defer func() {
		err := acc.Close()
		log.Check(err, "closing account")
	}()

	r = r.WithContext(context.WithValue(r.Context(), accountCtxKey, acc))
	h.serve(w, r)
}

func (h handler) serve(w http.ResponseWriter, r *http.Request) {
	acc := accountFrom(r)
	if acc == nil {
		http.Error(w, "500 - internal server error - missing account", http.StatusInternalServerError)
		return
	}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}

	res := parseResource(path)
	if res.account != "" && res.account != acc.Name {
		http.Error(w, "403 - forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.serveGet(w, r, res)
	case http.MethodPut:
		h.servePut(w, r, res)
	case http.MethodDelete:
		h.serveDelete(w, r, res)
	case "PROPFIND":
		h.propfind(w, r, acc, res)
	default:
		http.NotFound(w, r)
	}
}

func (h handler) serveGet(w http.ResponseWriter, r *http.Request, res resource) {
	switch res.kind {
	case resRoot, resPrincipal, resCalendarHome, resCalendar:
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	case resObject:
		h.serveObjectGet(w, r, res)
	default:
		http.NotFound(w, r)
	}
}

func (h handler) authenticate(log mlog.Log, w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	email, password, aok := r.BasicAuth()
	if !aok {
		log.Debug("missing http basic authentication credentials")
		w.Header().Set("WWW-Authenticate", "Basic realm=caldav")
		http.Error(w, "401 - unauthorized - use http basic auth with email address as username", http.StatusUnauthorized)
		return nil, false
	}
	log = log.With(slog.String("username", email))

	t0 := time.Now()

	clientIP := webauth.ClientIP(log, h.isForwarded, r)
	if clientIP == nil {
		log.Debug("cannot find remote ip for rate limiter")
		http.Error(w, "500 - internal server error - cannot find remote ip", http.StatusInternalServerError)
		return nil, false
	}
	if !mox.LimiterFailedAuth.CanAdd(clientIP, t0, 1) {
		metrics.AuthenticationRatelimitedInc("caldav")
		log.Debug("refusing connection due to many auth failures", slog.Any("clientip", clientIP))
		http.Error(w, "429 - too many auth attempts", http.StatusTooManyRequests)
		return nil, false
	}

	la := loginAttempt(clientIP.String(), r, "caldav", "httpbasic")
	la.LoginAddress = email
	defer func() {
		store.LoginAttemptAdd(context.Background(), log, la)
	}()

	var acc *store.Account
	var err error
	acc, la.AccountName, err = store.OpenEmailAuth(log, email, password, true)
	if err != nil {
		mox.LimiterFailedAuth.Add(clientIP, t0, 1)
		if errors.Is(err, mox.ErrDomainNotFound) || errors.Is(err, mox.ErrAddressNotFound) || errors.Is(err, store.ErrUnknownCredentials) || errors.Is(err, store.ErrLoginDisabled) {
			log.Debug("bad http basic authentication credentials")
			la.Result = store.AuthBadCredentials
			msg := "use http basic auth with email address as username"
			if errors.Is(err, store.ErrLoginDisabled) {
				la.Result = store.AuthLoginDisabled
				msg = "login is disabled for this account"
			}
			w.Header().Set("WWW-Authenticate", "Basic realm=caldav")
			http.Error(w, "401 - unauthorized - "+msg, http.StatusUnauthorized)
			return nil, false
		}
		http.Error(w, "500 - internal server error - error verifying credentials", http.StatusInternalServerError)
		return nil, false
	}
	la.AccountName = acc.Name
	la.Result = store.AuthSuccess
	mox.LimiterFailedAuth.Reset(clientIP, t0)
	return acc, true
}

func loginAttempt(clientIP string, r *http.Request, protocol, authMech string) store.LoginAttempt {
	return store.LoginAttempt{
		RemoteIP:  clientIP,
		TLS:       store.LoginAttemptTLS(r.TLS),
		Protocol:  protocol,
		AuthMech:  authMech,
		UserAgent: r.UserAgent(),
		Result:    store.AuthError,
	}
}
