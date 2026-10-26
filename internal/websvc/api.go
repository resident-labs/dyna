package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"path/filepath"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/settings"

	"dyna-agent/internal/websvc/devicestore"
)

func (s *Server) cookie(rt Route, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: devicestore.CookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		// The route, not a request header, says whether the browser is on
		// HTTPS: tailscale serve and cloudflared both end TLS.
		Secure: strings.HasPrefix(rt.App, "https://"),
	}
}

type pairRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request, rt Route) {
	if err := csrf(r, rt); err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPairBody)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "bad pairing request")
		return
	}
	// A fixed delay on every try, right or wrong.
	time.Sleep(s.o.PairDelay)
	tok, dev, err := s.o.Store.Pair(req.Code, req.Name)
	if errors.Is(err, devicestore.ErrPair) {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		s.o.Logf("web: pairing failed: %v", err)
		jsonError(w, http.StatusInternalServerError, "pairing failed on the server")
		return
	}
	// A pairing code names the scopes the owner chose. No scopes means the
	// owner chose none, and the route says who asks: this computer's browser
	// is the owner's own and is trusted like a shell, so it gets every
	// scope. A device paired from anywhere else gets the default grant.
	scopes := dev.Scopes
	if len(scopes) == 0 {
		if LocalRoute(rt) {
			scopes = scope.All()
		} else {
			scopes = scope.Default()
		}
	}
	grants, err := devicegrant.Open(filepath.Dir(s.o.Store.Dir()))
	if err == nil {
		err = grants.Put("paired", dev.ID, scopes, false)
	}
	if err != nil {
		s.o.Logf("web: pairing grant failed: %v", err)
		jsonError(w, http.StatusInternalServerError, "pairing failed on the server")
		return
	}
	s.o.Logf("web: paired device %s (%s)", dev.ID, dev.Name)
	http.SetCookie(w, s.cookie(rt, tok, int(devicestore.DeviceLife/time.Second)))
	jsonReply(w, http.StatusOK, map[string]string{"device": dev.ID, "name": dev.Name})
}

// ErrReadOnly is a write method's answer in an engine that runs read-only.
var ErrReadOnly = errors.New("read-only mode")

type callErrorResponse struct {
	Error string `json:"error"`
	// Code names a reply a caller acts on, such as callspec.CodeEngineStopping.
	Code  string                 `json:"code,omitempty"`
	Close *sessionclose.Recovery `json:"close,omitempty"`
}

type closeRefusal interface {
	error
	CloseRecovery() sessionclose.Recovery
}

func writeCallError(w http.ResponseWriter, status int, code string, err error) {
	var refusal closeRefusal
	if errors.As(err, &refusal) {
		recovery := refusal.CloseRecovery()
		jsonReply(w, status, callErrorResponse{Error: err.Error(), Code: code, Close: &recovery})
		return
	}
	jsonReply(w, status, callErrorResponse{Error: err.Error(), Code: code})
}

// callFailure is the status and code for a failed method. A call whose request
// context ended was canceled by the engine, which is stopping, or by the caller.
// Whatever text its error carries, the same request may be sent again, so it
// gets 503 with callspec.CodeEngineStopping and never a final 422 (D-269).
func callFailure(requestErr, err error) (status int, code string) {
	if requestErr != nil {
		return http.StatusServiceUnavailable, callspec.CodeEngineStopping
	}
	return methodStatus(err), ""
}

// methodStatus is the HTTP status for a method's error. An error that can
// pass gets 503, so a caller may send the same request again: a write
// deadline, a busy or locked database, a read-only engine, or a request
// the stopping engine canceled. A refusal or bad input gets 422.
func methodStatus(err error) int {
	if Transient(err) {
		return http.StatusServiceUnavailable
	}
	return http.StatusUnprocessableEntity
}

// Transient reports whether a method's error can pass, so the same request
// may be sent again: a write deadline, a busy or locked database, a
// read-only engine, or a request the stopping engine canceled.
func Transient(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrReadOnly) || errors.Is(err, callspec.ErrRetryable) || database.Busy(err)
}

var (
	transientLogMu   sync.Mutex
	transientLogLast = map[string]time.Time{}
)

// logTransient records the error behind a 503 at most once per method per
// minute, so a retry loop leaves a trace without flooding the log.
func logTransient(method string, err error) {
	transientLogMu.Lock()
	defer transientLogMu.Unlock()
	if time.Since(transientLogLast[method]) < time.Minute {
		return
	}
	transientLogLast[method] = time.Now()
	log.Printf("websvc: %s answered 503 (transient error): %v", method, err)
}

func (s *Server) call(w http.ResponseWriter, r *http.Request, rt Route, p Principal, name string) {
	if err := csrf(r, rt); err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}
	limits, err := settings.Content()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var args []json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limits.JSONBytes())).Decode(&args); err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("the body must be a JSON array of arguments (maximum %s): %v", settings.FormatBytes(limits.JSONBytes()), err))
		return
	}
	if name == "CardURL" {
		if p.Kind != "local" && !p.Allows(ScopeRead) {
			jsonError(w, http.StatusForbidden, "this device cannot use CardURL")
			return
		}
		s.cardURL(w, rt, args)
		return
	}
	if s.o.Version != "" {
		w.Header().Set(callspec.VersionHeader, s.o.Version)
	}
	c, ok := s.o.Calls[name]
	if !ok {
		// A dyna command from another build may name a method this engine
		// does not have; say which builds meet.
		if client := r.Header.Get(callspec.VersionHeader); client != "" && client != s.o.Version {
			jsonError(w, http.StatusNotFound, fmt.Sprintf("%s (the engine has no method %s)", callspec.VersionGap(s.o.Version, client), name))
			return
		}
		jsonError(w, http.StatusNotFound, "the web client has no method "+name)
		return
	}
	if p.Kind != "local" {
		need, tagged := s.o.CallScopes[name]
		if !tagged || !p.Allows(need) {
			jsonError(w, http.StatusForbidden, "this device cannot use "+name)
			return
		}
	}
	ctx := r.Context()
	if id := r.Header.Get(callspec.RequestHeader); id != "" {
		ctx = callspec.WithRequest(ctx, id)
	}
	res, err := c(ctx, args)
	if err != nil {
		status, code := callFailure(r.Context().Err(), err)
		if status == http.StatusServiceUnavailable && !errors.Is(r.Context().Err(), context.Canceled) {
			logTransient(name, err)
		}
		writeCallError(w, status, code, err)
		return
	}
	if result, ok := res.(PrincipalResult); ok {
		res = result.ForPrincipal(p)
	}
	// A room's feed is asked for again on every event and mostly comes back unchanged.
	gzipJSONReply(w, r, http.StatusOK, res, name == "FeedTail")
}
