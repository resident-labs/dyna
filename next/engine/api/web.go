package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type Web struct {
	mu          sync.Mutex
	host        string
	routes      map[string]string
	discovered  string
	discovery   func(context.Context, int) (string, error)
	port        int
	routeWake   func()
	discoveryAt time.Time
	discoverMu  sync.Mutex
	address     string
	handler     http.Handler
	muxOnce     sync.Once
	mux         http.Handler
	now         func() time.Time
	ticks       func() (<-chan time.Time, func())
	tokens      map[[32]byte]time.Time
	devices     DeviceStore
	connect     ConnectSessions
	app         fs.FS
	api         *API
}

type DeviceStore interface {
	CreateDevice(context.Context, store.Device) error
	AuthenticateDevice(context.Context, [32]byte, time.Time) (store.Device, bool, error)
	RedeemPairCode(context.Context, string, string, []contract.Scope, time.Time) (string, store.Device, error)
}

func NewWeb(a *API, address string, app fs.FS, now func() time.Time, devices DeviceStore) (*Web, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return nil, fmt.Errorf("web listener must bind 127.0.0.1 with a port")
	}
	w := &Web{address: address, host: "dyna.localhost:" + port, routes: map[string]string{"dyna.localhost:" + port: "http://dyna.localhost:" + port}, now: now, app: app, api: a, tokens: make(map[[32]byte]time.Time), devices: devices}
	w.ticks = func() (<-chan time.Time, func()) {
		t := time.NewTicker(15 * time.Second)
		return t.C, t.Stop
	}
	a.web = w
	return w, nil
}

// ConnectSessions is the authentication boundary for relay-backed sessions.
type ConnectSessions interface {
	ManagedOrigin(context.Context) string
	ServePublic(http.ResponseWriter, *http.Request, string, []string) bool
	ServePrivate(http.ResponseWriter, *http.Request, string, store.ConnectSession, []string)
	Authenticate(context.Context, *http.Request, string) (store.ConnectSession, []contract.Scope, bool, error)
	StreamScopes(context.Context, string) ([]contract.Scope, bool, error)
}

// SetConnect installs Connect session authorization before the listener starts.
func (w *Web) SetConnect(s ConnectSessions) { w.connect = s }

// SetRoutes adds the public origins served by the loopback listener.
func (w *Web) SetRoutes(routes []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, route := range routes {
		u, err := url.Parse(route)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || (route != u.Scheme+"://"+u.Host && route != u.Scheme+"://"+u.Host+"/") || strings.ContainsAny(route, "*; \t\r\n") {
			return fmt.Errorf("invalid web route %q: expected an http or https origin", route)
		}
		host := strings.ToLower(u.Host)
		origin := u.Scheme + "://" + host
		if existing, ok := w.routes[host]; ok && existing != origin {
			return fmt.Errorf("web route %q has a conflicting host", route)
		}
		w.routes[host] = origin
	}
	return nil
}

// SetDiscovery installs bounded route discovery.
func (w *Web) SetDiscovery(discover func(context.Context, int) (string, error), wake func()) {
	w.discoverMu.Lock()
	defer w.discoverMu.Unlock()
	w.discovery, w.routeWake = discover, wake
}

func (w *Web) discover(ctx context.Context, wake bool) {
	w.discoverMu.Lock()
	defer w.discoverMu.Unlock()
	if w.discovery == nil || (!w.discoveryAt.IsZero() && w.now().Before(w.discoveryAt.Add(30*time.Second))) {
		return
	}
	w.discoveryAt = w.now()
	route, err := w.discovery(context.WithoutCancel(ctx), w.port)
	if err != nil {
		return
	}
	old := w.discovered
	host := strings.ToLower(strings.TrimPrefix(route, "https://"))
	w.mu.Lock()
	delete(w.routes, old)
	_, configured := w.routes[host]
	w.mu.Unlock()
	w.discovered = ""
	if route != "" && w.SetRoutes([]string{route}) == nil && !configured {
		w.discovered = host
	}
	if wake && old != w.discovered && w.routeWake != nil {
		w.routeWake()
	}
}

// RefreshRoutes refreshes discovered routes for a tunnel report.
func (w *Web) RefreshRoutes(ctx context.Context) []string {
	w.discover(ctx, false)
	return w.PublicRoutes()
}

// PublicRoutes lists the origins set with SetRoutes, without the loopback one.
func (w *Web) PublicRoutes() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := []string{}
	for host, origin := range w.routes {
		if host != w.host {
			out = append(out, origin)
		}
	}
	slices.Sort(out)
	return out
}

func (w *Web) routeOrigins(ctx context.Context) ([]string, string) {
	w.mu.Lock()
	var routes []string
	for _, route := range w.routes {
		routes = append(routes, route)
	}
	w.mu.Unlock()
	var managed string
	if w.connect != nil {
		managed = w.connect.ManagedOrigin(ctx)
		if managed != "" && !slices.Contains(routes, managed) {
			routes = append(routes, managed)
		}
	}
	return routes, managed
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
	routes, managed := w.routeOrigins(r.Context())
	out.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; img-src 'self' blob:; connect-src 'self' "+strings.Join(routes, " ")+"; frame-ancestors 'none'")
	out.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("Tailscale-Funnel-Request") != "" {
		http.Error(out, "forbidden", http.StatusForbidden)
		return
	}
	host := strings.ToLower(r.Host)
	w.mu.Lock()
	origin, known := w.routes[host]
	w.mu.Unlock()
	if !known && w.connect != nil {
		if u, err := url.Parse(managed); err == nil && strings.EqualFold(u.Host, r.Host) && (u.Scheme == "https" || u.Scheme == "http") {
			origin, known = managed, true
		}
	}
	if !known {
		w.discover(r.Context(), true)
		w.mu.Lock()
		origin, known = w.routes[host]
		w.mu.Unlock()
	}
	if !known {
		http.Error(out, "unknown host", http.StatusMisdirectedRequest)
		return
	}
	relayRequest := r.URL.Path == "/connect/v1/mint" || r.URL.Path == "/connect/v1/revoke"
	if !relayRequest && (r.Method != http.MethodGet && r.Method != http.MethodHead || strings.EqualFold(r.Header.Get("Upgrade"), "websocket")) && r.Header.Get("Origin") != origin {
		http.Error(out, "forbidden", http.StatusForbidden)
		return
	}
	if w.connect != nil {
		if w.connect.ServePublic(out, r, origin, routes) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/connect/") || r.URL.Path == "/api/logout" {
			x, _, ok, err := w.connect.Authenticate(r.Context(), r, origin)
			if err != nil {
				http.Error(out, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if !ok {
				http.Error(out, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.connect.ServePrivate(out, r, origin, x, routes)
			return
		}
	}
	if r.URL.Path == "/v1/pair" && r.Method == http.MethodPost {
		var req struct {
			Code string `json:"code"`
			Name string `json:"name"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(out, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || len(req.Name) > 200 {
			http.Error(out, "invalid request", http.StatusBadRequest)
			return
		}
		if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(out, "invalid request", http.StatusBadRequest)
			return
		}
		defaults := []contract.Scope{contract.ScopeRead, contract.ScopeOperate}
		if origin == "http://"+w.host {
			defaults = store.FullDeviceScopes
		}
		value, _, err := w.devices.RedeemPairCode(r.Context(), req.Code, req.Name, defaults, w.now())
		if errors.Is(err, store.ErrPairEnded) {
			http.Error(out, "pairing code ended", http.StatusGone)
			return
		}
		if errors.Is(err, store.ErrPair) {
			http.Error(out, "invalid pairing code", http.StatusForbidden)
			return
		}
		if err != nil {
			http.Error(out, "unavailable", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(out, &http.Cookie{Name: "dyna_device", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(origin, "https://"), MaxAge: int(store.DeviceLife / time.Second)})
		out.Header().Set("Content-Type", "application/json")
		_, _ = out.Write([]byte("{}"))
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
		http.SetCookie(out, &http.Cookie{Name: "dyna_device", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(origin, "https://"), MaxAge: int(store.DeviceLife / time.Second)})
		http.Redirect(out, r, "/", http.StatusSeeOther)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		cookie, _ := r.Cookie("dyna_device")
		var device store.Device
		var valid bool
		var err error
		if cookie != nil {
			device, valid, err = w.devices.AuthenticateDevice(r.Context(), sha256.Sum256([]byte(cookie.Value)), w.now())
		}
		if err != nil {
			http.Error(out, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var session store.ConnectSession
		if !valid && w.connect != nil {
			var scopes []contract.Scope
			session, scopes, valid, err = w.connect.Authenticate(r.Context(), r, origin)
			if err != nil {
				http.Error(out, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if valid && session.Stale {
				out.Header().Set("Content-Type", "application/json")
				out.WriteHeader(http.StatusUnauthorized)
				_, _ = out.Write([]byte(`{"error":"this session needs to renew","code":"renew"}`))
				return
			}
			device.ID, device.Scopes = session.Device, scopes
		}
		if !valid {
			http.Error(out, "forbidden", http.StatusForbidden)
			return
		}
		if session.ID == "" {
			http.SetCookie(out, &http.Cookie{Name: "dyna_device", Value: cookie.Value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(origin, "https://"), MaxAge: int(store.DeviceLife / time.Second)})
		}
		var streamScope contract.Scope
		switch r.URL.Path {
		case "/v1/terminal":
			streamScope = contract.ScopeTerminal
		case "/v1/computers/watch":
			streamScope = contract.ScopeComputers
		case "/v1/events":
			streamScope = contract.ScopeRead
		case "/v1/shell/watch":
			streamScope = contract.ScopeTerminal
		}
		ctx := r.Context()
		if streamScope != "" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			ticks, stop := w.ticks()
			defer stop()
			// ctx is a parameter because the request path reassigns the outer one below.
			go func(ctx context.Context) {
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticks:
						var scopes []contract.Scope
						var alive bool
						var err error
						if session.ID != "" {
							scopes, alive, err = w.connect.StreamScopes(ctx, session.ID)
						} else {
							var current store.Device
							current, alive, err = w.devices.AuthenticateDevice(ctx, sha256.Sum256([]byte(cookie.Value)), w.now())
							scopes = current.Scopes
						}
						if err != nil || !alive || (streamScope != "" && !hasScope(Caller{Web: true, Scopes: scopes}, streamScope)) {
							cancel()
							return
						}
					}
				}
			}(ctx)
		}
		ctx = context.WithValue(ctx, callerKey{}, Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner, Device: contract.DeviceID(device.ID)}, Device: device.ID, Scopes: device.Scopes, Web: true})
		handler := w.handler
		if handler == nil {
			w.muxOnce.Do(func() {
				w.mux = w.api.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true})
			})
			handler = w.mux
		}
		handler.ServeHTTP(out, r.WithContext(ctx))
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
	l, err := net.Listen("tcp", w.address)
	if err != nil {
		return nil, err
	}
	w.port = l.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: w, ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(l)
	return l, nil
}
