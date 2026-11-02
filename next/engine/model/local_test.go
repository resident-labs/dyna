package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract/private"
)

type localFake struct {
	key      string
	accounts []private.AccountBinding
	err      error
}

func (s localFake) LocalProxyKey(context.Context) (string, error) { return s.key, s.err }
func (s localFake) Accounts(context.Context) ([]private.AccountBinding, error) {
	return s.accounts, nil
}

func twoAccounts() []private.AccountBinding {
	return []private.AccountBinding{{ProxyID: "credential"}, {ProxyID: "other"}}
}

// localSend sends one request through the local route and reports what reached the proxy.
func localSend(t *testing.T, st LocalStore, method, path, body string, headers map[string]string) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var seen *http.Request
	up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	proxy := &Proxy{client: unixClient(up.Listener.Addr().String()), clientKey: "secret"}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	proxy.Local(st).ServeHTTP(w, req)
	return w, seen
}

func TestLocalRouteRefusesAnyOtherKey(t *testing.T) {
	stored := localFake{key: "owner-key", accounts: twoAccounts()}
	for name, c := range map[string]struct {
		st      localFake
		headers map[string]string
	}{
		"none":             {stored, nil},
		"wrong bearer":     {stored, map[string]string{"Authorization": "Bearer other"}},
		"wrong api key":    {stored, map[string]string{"X-Api-Key": "other"}},
		"not a bearer":     {stored, map[string]string{"Authorization": "owner-key"}},
		"none stored":      {localFake{accounts: twoAccounts()}, map[string]string{"Authorization": "Bearer "}},
		"wrong beats good": {stored, map[string]string{"Authorization": "Bearer other", "X-Api-Key": "owner-key"}},
	} {
		t.Run(name, func(t *testing.T) {
			w, seen := localSend(t, c.st, "POST", "/model/v1/messages", `{"model":"any"}`, c.headers)
			if w.Code != http.StatusUnauthorized || seen != nil || strings.Contains(w.Body.String(), "owner-key") {
				t.Fatalf("status %d, reached proxy %v, body %s", w.Code, seen != nil, w.Body)
			}
		})
	}
}

func TestLocalRouteIsTheBoxRoutesPolicyWithoutAProfile(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"bearer":  {"Authorization": "Bearer owner-key"},
		"api key": {"X-Api-Key": "owner-key"},
	} {
		t.Run(name, func(t *testing.T) {
			headers["X-Dyna-Pinned-Auth"] = "spoof"
			headers["X-Dyna-Eligible-Auth"] = "spoof"
			headers["X-Claude-Code-Session-Id"] = "ses_spoof"
			w, seen := localSend(t, localFake{key: "owner-key", accounts: twoAccounts()}, "POST", "/model/v1/messages?beta=true&key=owner-key", `{"model":"in-no-profile"}`, headers)
			if w.Code != http.StatusNoContent || seen == nil {
				t.Fatalf("status %d", w.Code)
			}
			h := seen.Header
			if h.Get("Authorization") != "Bearer secret" || h.Get("X-Api-Key") != "secret" {
				t.Fatalf("the owner's key was forwarded: %v", h)
			}
			if h.Get("X-Claude-Code-Session-Id") != "local" || h.Get("X-Dyna-Eligible-Auth") != "Y3JlZGVudGlhbA,b3RoZXI" || h.Get("X-Dyna-Pinned-Auth") != "" {
				t.Fatalf("policy headers: %v", h)
			}
			if seen.URL.Path != "/v1/messages" || seen.URL.RawQuery != "beta=true" {
				t.Fatalf("forwarded %s", seen.URL)
			}
		})
	}
}

func TestLocalRouteServesWhatHarnessesCall(t *testing.T) {
	st := localFake{key: "owner-key", accounts: twoAccounts()}
	auth := map[string]string{"Authorization": "Bearer owner-key"}
	for _, c := range []struct {
		method, path, want string
		status             int
	}{
		{"POST", "/model/v1/messages/count_tokens", "/v1/messages/count_tokens", http.StatusNoContent},
		{"POST", "/model/v1/chat/completions", "/v1/chat/completions", http.StatusNoContent},
		{"POST", "/model/v1/responses", "/v1/responses", http.StatusNoContent},
		{"POST", "/model/v1/responses/compact", "/v1/responses/compact", http.StatusNoContent},
		{"GET", "/model/v1/models", "/v1/models", http.StatusNoContent},
		{"GET", "/model/v1/messages", "", http.StatusMethodNotAllowed},
		{"POST", "/model/v1/models", "", http.StatusMethodNotAllowed},
		{"POST", "/model/v0/management/config", "", http.StatusNotFound},
		{"POST", "/model/dyna/keys", "", http.StatusNotFound},
	} {
		w, seen := localSend(t, st, c.method, c.path, `{"model":"any"}`, auth)
		if w.Code != c.status || (seen == nil) != (c.want == "") || seen != nil && seen.URL.Path != c.want {
			t.Fatalf("%s %s: status %d, reached %v", c.method, c.path, w.Code, seen)
		}
	}
}

func TestLocalRouteSaysWhenItCannotServe(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer owner-key"}
	w, seen := localSend(t, localFake{key: "owner-key"}, "POST", "/model/v1/messages", `{"model":"any"}`, auth)
	if w.Code != http.StatusServiceUnavailable || seen != nil || !strings.Contains(w.Body.String(), "no_account") {
		t.Fatalf("no accounts: status %d, body %s", w.Code, w.Body)
	}
	w, seen = localSend(t, localFake{err: errors.New("store")}, "POST", "/model/v1/messages", `{"model":"any"}`, auth)
	if w.Code != http.StatusServiceUnavailable || seen != nil {
		t.Fatalf("store failure: status %d", w.Code)
	}
}

// What a harness needs must not widen what a box may call.
func TestBoxRouteKeepsItsThreePaths(t *testing.T) {
	called := false
	up := unixServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer up.Close()
	route := Handler(up.Listener.Addr().String(), "secret", "session", []string{"allowed"}, []string{"credential"}, "", false)
	for _, path := range []string{"/model/v1/models", "/model/v1/messages/count_tokens", "/model/v1/responses/compact"} {
		w := httptest.NewRecorder()
		route.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"model":"allowed"}`)))
		if w.Code != http.StatusNotFound || called {
			t.Fatalf("%s: status %d, called %v", path, w.Code, called)
		}
	}
}
