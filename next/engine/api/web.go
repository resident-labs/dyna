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
	"github.com/resident-labs/dyna/engine/store"
)

type Web struct {
	mu      sync.Mutex
	host    string
	address string
	handler http.Handler
	now     func() time.Time
	tokens  map[[32]byte]time.Time
	devices DeviceStore
	app     fs.FS
	api     *API
}

type DeviceStore interface {
	CreateDevice(context.Context, store.Device) error
	AuthenticateDevice(context.Context, [32]byte, time.Time) (store.Device, bool, error)
}

func NewWeb(a *API, address string, app fs.FS, now func() time.Time, devices DeviceStore) (*Web, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, fmt.Errorf("web listener must bind 127.0.0.1 with a port")
	}
	w := &Web{address: address, host: "dyna.localhost:" + port, now: now, app: app, api: a, tokens: make(map[[32]byte]time.Time), devices: devices}
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
		id, err := secret()
		if err != nil {
			http.Error(out, "unavailable", http.StatusServiceUnavailable)
			return
		}
		now := w.now()
		d := store.Device{ID: id, Name: "Local web", TokenHash: sha256.Sum256([]byte(value)), PairedAt: now, LastSeenAt: now, ExpiresAt: now.Add(store.DeviceLife), Scopes: store.FullDeviceScopes}
		if err := w.devices.CreateDevice(r.Context(), d); err != nil {
			http.Error(out, "unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(out, &http.Cookie{Name: "dyna_device", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(store.DeviceLife / time.Second)})
		http.Redirect(out, r, "/", http.StatusSeeOther)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		cookie, err := r.Cookie("dyna_device")
		if err != nil {
			http.Error(out, "forbidden", http.StatusForbidden)
			return
		}
		_, valid, err := w.devices.AuthenticateDevice(r.Context(), sha256.Sum256([]byte(cookie.Value)), w.now())
		if err != nil {
			http.Error(out, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if !valid {
			http.Error(out, "forbidden", http.StatusForbidden)
			return
		}
		http.SetCookie(out, &http.Cookie{Name: "dyna_device", Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(store.DeviceLife / time.Second)})
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
