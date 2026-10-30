package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	now     func() time.Time
	tokens  map[[32]byte]time.Time
	cookies map[[32]byte]struct{}
	app     fs.FS
	api     *API
}

func NewWeb(a *API, port string, app fs.FS, now func() time.Time) *Web {
	w := &Web{host: "dyna.localhost:" + port, now: now, app: app, api: a, tokens: make(map[[32]byte]time.Time), cookies: make(map[[32]byte]struct{})}
	a.web = w
	return w
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
	out.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
	out.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host != w.host || (r.Method != http.MethodGet && r.Header.Get("Origin") != "http://"+w.host) {
		http.Error(out, "forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/login" && r.Method == http.MethodGet {
		hash := sha256.Sum256([]byte(r.URL.Query().Get("t")))
		w.mu.Lock()
		valid := 0
		for candidate, expiry := range w.tokens {
			match := subtle.ConstantTimeCompare(hash[:], candidate[:])
			if match == 1 {
				delete(w.tokens, candidate)
				if w.now().Before(expiry) {
					valid = 1
				}
			}
		}
		w.mu.Unlock()
		if valid != 1 {
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
	if r.URL.Path == "/v1/web.login" {
		http.NotFound(out, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		cookie, err := r.Cookie("dyna_web")
		valid := 0
		if err == nil {
			hash := sha256.Sum256([]byte(cookie.Value))
			w.mu.Lock()
			for candidate := range w.cookies {
				valid |= subtle.ConstantTimeCompare(hash[:], candidate[:])
			}
			w.mu.Unlock()
		}
		if valid != 1 {
			http.Error(out, "forbidden", http.StatusForbidden)
			return
		}
		w.api.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}}).ServeHTTP(out, r)
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
	http.ServeContent(out, r, path, time.Time{}, strings.NewReader(string(data)))
}

func (w *Web) Listen(address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || "dyna.localhost:"+port != w.host {
		return nil, fmt.Errorf("web listener must bind 127.0.0.1 and its configured port")
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	go http.Serve(l, w)
	return l, nil
}
