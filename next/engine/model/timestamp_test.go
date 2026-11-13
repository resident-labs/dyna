package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelHandoffBoundary(t *testing.T) {
	sent := 0
	upstream := 0
	up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream++
		if sent != upstream {
			t.Errorf("handoff not recorded before proxy received request: sent=%d received=%d", sent, upstream)
		}
		// Even a child rejection counts as a handoff, but a route policy rejection does not.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer up.Close()
	record := func(context.Context) error { sent++; return nil }
	for _, tc := range []struct {
		method, path, body string
		ids                []string
		want               int
	}{
		{"POST", "/model/v1/messages", `{"model":"forbidden"}`, []string{"account"}, 0},
		{"POST", "/model/v1/messages", `{"model":"allowed"}`, nil, 0},
		{"GET", "/model/v1/responses", `{"model":"allowed"}`, []string{"account"}, 0},
		{"POST", "/model/not-inference", `{"model":"allowed"}`, []string{"account"}, 0},
		{"POST", "/model/v1/messages", `{"model":"allowed"}`, []string{"account"}, 1},
		{"POST", "/model/v1/chat/completions", `{"model":"allowed","stream":true}`, []string{"account"}, 2},
		{"POST", "/model/v1/responses", `{"model":"allowed","stream":true}`, []string{"account"}, 3},
		{"POST", "/model/v1/responses", `{"model":"allowed","stream":true}`, []string{"account"}, 4},
	} {
		w := httptest.NewRecorder()
		Handler(up.Listener.Addr().String(), "key", "trusted-session", []string{"allowed"}, tc.ids, "", nil, false, record).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if sent != tc.want {
			t.Fatalf("%s %s: sent=%d want=%d", tc.method, tc.path, sent, tc.want)
		}
	}
}

func TestModelHandoffFailureStillForwards(t *testing.T) {
	calls := 0
	up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusAccepted) }))
	defer up.Close()
	route := Handler(up.Listener.Addr().String(), "key", "trusted-session", []string{"allowed"}, []string{"account"}, "", nil, false, func(context.Context) error { return errors.New("store failed") })
	w := httptest.NewRecorder()
	route.ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed"}`)))
	if w.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("status=%d upstream=%d", w.Code, calls)
	}
}
