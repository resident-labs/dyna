package model

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
)

func TestRouteSwapsKeyAndSetsAffinity(t *testing.T) {
	up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Api-Key") != "secret" || r.Header.Get("X-Claude-Code-Session-Id") != "session" || r.Header.Get("X-Dyna-Eligible-Auth") != "Y3JlZGVudGlhbA" {
			t.Errorf("wrong request: %s %v", r.URL.Path, r.Header)
		}
		w.WriteHeader(204)
	}))
	defer up.Close()
	target := up.Listener.Addr().String()
	req := httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed"}`))
	req.Header.Set("Connection", "X-Dyna-Eligible-Auth")
	req.Header.Set("X-Dyna-Eligible-Auth", "spoof")
	req.Header.Set("X-Api-Key", "box")
	req.Header.Set("Authorization", "Bearer box")
	w := httptest.NewRecorder()
	Handler(target, "secret", "session", []string{"allowed"}, []string{"credential"}).ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestRouteRefusesForbiddenModel(t *testing.T) {
	called := false
	up := unixServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer up.Close()
	target := up.Listener.Addr().String()
	w := httptest.NewRecorder()
	Handler(target, "secret", "session", nil, nil).ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"other"}`)))
	if w.Code != 403 || called {
		t.Fatalf("status %d, called %v", w.Code, called)
	}
}

func TestRouteStreamsAndDoesNotRetry(t *testing.T) {
	calls := 0
	up := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(503)
		io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: two\n\n")
	}))
	defer up.Close()
	target := up.Listener.Addr().String()
	w := httptest.NewRecorder()
	Handler(target, "secret", "session", []string{"allowed"}, []string{"credential"}).ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed"}`)))
	if calls != 1 || w.Code != 503 || w.Body.String() != "data: one\n\ndata: two\n\n" || !w.Flushed {
		t.Fatalf("calls %d status %d body %q flushed %v", calls, w.Code, w.Body.String(), w.Flushed)
	}
}

func TestRouteRejectsURLSelectedModel(t *testing.T) {
	called := false
	up := unixServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer up.Close()
	target := up.Listener.Addr().String()
	w := httptest.NewRecorder()
	Handler(target, "secret", "session", []string{"allowed"}, []string{"credential"}).ServeHTTP(w, httptest.NewRequest("POST", "/model/v1beta/models/forbidden:generateContent", strings.NewReader(`{"model":"allowed"}`)))
	if w.Code != http.StatusNotFound || called {
		t.Fatalf("status %d, called %v", w.Code, called)
	}
}

func TestRouteRejectsAmbiguousModel(t *testing.T) {
	target := filepath.Join(t.TempDir(), "unused.sock")
	w := httptest.NewRecorder()
	Handler(target, "secret", "session", []string{"allowed"}, []string{"credential"}).ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed","model":"other"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

func TestProxyConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cliproxy")
	proxy, err := NewProxy(dir, filepath.Join(dir, "auth-v2"), "management-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Listener.Close() })
	path := filepath.Join(dir, "config.yaml")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config mode: %v, %v", info, err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// Check 7: StrictMode lives only in claude-api-key cloak blocks, so no key means strict cloak stays off.
	if cfg.RequestRetry != 0 || !cfg.RemoteManagement.AllowRemote || cfg.RemoteManagement.SecretKey == "" || len(cfg.APIKeys) != 1 || !cfg.Routing.SessionAffinity || cfg.Routing.Strategy != "soonest-reset" || len(cfg.ClaudeKey) != 0 {
		t.Fatal("proxy config violates the retry, management, or affinity policy")
	}
	if bcrypt.CompareHashAndPassword([]byte(cfg.RemoteManagement.SecretKey), []byte("management-key")) != nil {
		t.Fatal("management key does not authenticate")
	}
	info, err = os.Stat(filepath.Join(dir, "auth-v2"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("auth mode: %v, %v", info, err)
	}
}

func unixServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	up := httptest.NewUnstartedServer(handler)
	_ = up.Listener.Close()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "up.sock"))
	if err != nil {
		t.Fatal(err)
	}
	up.Listener = listener
	up.Start()
	return up
}

func TestProxyPreservesNonSocket(t *testing.T) {
	dir := t.TempDir()
	proxyDir := filepath.Join(dir, "cliproxy")
	if err := os.Mkdir(proxyDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proxyDir, "proxy.sock")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if proxy, err := NewProxy(proxyDir, filepath.Join(proxyDir, "auth-v2"), "key"); err == nil {
		proxy.Listener.Close()
		t.Fatal("accepted non-socket")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("file changed: %q %v", data, err)
	}
}
