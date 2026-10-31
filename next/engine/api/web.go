package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type Web struct {
	mu      sync.Mutex
	host    string
	address string
	handler http.Handler
	now     func() time.Time
	tokens  map[[32]byte]time.Time
	cookies map[[32]byte]struct{}
	app     fs.FS
	api     *API
}

func NewWeb(a *API, address string, app fs.FS, now func() time.Time) (*Web, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, fmt.Errorf("web listener must bind 127.0.0.1 with a port")
	}
	w := &Web{address: address, host: "dyna.localhost:" + port, now: now, app: app, api: a, tokens: make(map[[32]byte]time.Time), cookies: make(map[[32]byte]struct{})}
	a.web = w
	return w, nil
}

func secret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (w *Web) login(_ context.Context, _ contract.Principal, _ contract.WebLoginRequest) (contract.WebLoginResponse, *contract.Error) {
	token, err := secret()
	if err != nil {
		return contract.WebLoginResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "random token unavailable"}
	}
	w.mu.Lock()
	for hash, expiry := range w.tokens {
		if !w.now().Before(expiry) {
			delete(w.tokens, hash)
		}
	}
	w.tokens[sha256.Sum256([]byte(token))] = w.now().Add(time.Minute)
	w.mu.Unlock()
	return contract.WebLoginResponse{URL: "http://" + w.host + "/login?t=" + token}, nil
}

func (w *Web) ServeHTTP(out http.ResponseWriter, r *http.Request) {
	// The terminal renderer is WebAssembly; a composer shows a picked image from a blob: URL before any post refers to it.
	out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; img-src 'self' blob:; frame-ancestors 'none'")
	out.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host != w.host || (r.Method != http.MethodGet && r.Header.Get("Origin") != "http://"+w.host) {
		http.Error(out, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/login" && r.Method == http.MethodGet {
		hash := sha256.Sum256([]byte(r.URL.Query().Get("t")))
		w.mu.Lock()
		expiry, ok := w.tokens[hash]
		delete(w.tokens, hash)
		w.mu.Unlock()
		if !ok || !w.now().Before(expiry) {
			http.Error(out, "forbidden", http.StatusForbidden)
			return
		}
		value, err := secret()
		if err != nil {
			http.Error(out, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.mu.Lock()
		w.cookies[sha256.Sum256([]byte(value))] = struct{}{}
		w.mu.Unlock()
		http.SetCookie(out, &http.Cookie{Name: "dyna_web", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(out, r, "/", http.StatusSeeOther)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		cookie, err := r.Cookie("dyna_web")
		valid := false
		if err == nil {
			hash := sha256.Sum256([]byte(cookie.Value))
			w.mu.Lock()
			_, valid = w.cookies[hash]
			w.mu.Unlock()
		}
		if !valid {
			http.Error(out, "forbidden", http.StatusForbidden)
			return
		}
		w.handler.ServeHTTP(out, r)
		return
	}
	if r.Method != http.MethodGet {
		http.NotFound(out, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(w.app, path); err != nil {
		path = "index.html"
	}
	data, err := fs.ReadFile(w.app, path)
	if err != nil {
		http.NotFound(out, r)
		return
	}
	http.ServeContent(out, r, path, time.Time{}, bytes.NewReader(data))
}

func (w *Web) Listen() (net.Listener, error) {
	w.handler = w.api.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true})
	l, err := net.Listen("tcp", w.address)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: w, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(l)
	return l, nil
}
