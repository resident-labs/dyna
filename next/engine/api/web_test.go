package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type streamDeviceStore struct {
	live atomic.Bool
	read atomic.Bool
}

func (s *streamDeviceStore) CreateDevice(context.Context, store.Device) error { return nil }
func (s *streamDeviceStore) RedeemPairCode(context.Context, string, string, []contract.Scope, time.Time) (string, store.Device, error) {
	return "", store.Device{}, store.ErrPair
}
func (s *streamDeviceStore) AuthenticateDevice(context.Context, [32]byte, time.Time) (store.Device, bool, error) {
	var scopes []contract.Scope
	if s.read.Load() {
		scopes = []contract.Scope{contract.ScopeRead}
	}
	return store.Device{Scopes: scopes}, s.live.Load(), nil
}

func TestWebStreamEndsAfterDeviceRevocation(t *testing.T) {
	devices := &streamDeviceStore{}
	devices.live.Store(true)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, devices)
	if err != nil {
		t.Fatal(err)
	}
	tick := make(chan time.Time)
	w.ticks = func() (<-chan time.Time, func()) { return tick, func() {} }
	started, ended := make(chan struct{}), make(chan struct{})
	w.handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	r := httptest.NewRequest(http.MethodGet, "http://dyna.localhost:7790/v1/events", nil)
	r.AddCookie(&http.Cookie{Name: "dyna_device", Value: "token"})
	go func() { w.ServeHTTP(httptest.NewRecorder(), r); close(ended) }()
	<-started
	devices.live.Store(false)
	tick <- time.Time{}
	<-ended
}

func TestWebStreamEndsAfterScopeRemoval(t *testing.T) {
	devices := &streamDeviceStore{}
	devices.live.Store(true)
	devices.read.Store(true)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, devices)
	if err != nil {
		t.Fatal(err)
	}
	tick := make(chan time.Time)
	w.ticks = func() (<-chan time.Time, func()) { return tick, func() {} }
	started, ended := make(chan struct{}), make(chan struct{})
	w.handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	r := httptest.NewRequest(http.MethodGet, "http://dyna.localhost:7790/v1/events", nil)
	r.AddCookie(&http.Cookie{Name: "dyna_device", Value: "token"})
	go func() { w.ServeHTTP(httptest.NewRecorder(), r); close(ended) }()
	<-started
	devices.read.Store(false)
	tick <- time.Time{}
	<-ended
}

type streamConnect struct{ read atomic.Bool }

func (*streamConnect) ManagedOrigin(context.Context) string { return "" }
func (*streamConnect) ServePublic(http.ResponseWriter, *http.Request, string, []string) bool {
	return false
}
func (*streamConnect) ServePrivate(http.ResponseWriter, *http.Request, string, store.ConnectSession, []string) {
}
func (*streamConnect) Authenticate(context.Context, *http.Request, string) (store.ConnectSession, []contract.Scope, bool, error) {
	return store.ConnectSession{ID: "session", Device: "device"}, []contract.Scope{contract.ScopeRead}, true, nil
}
func (s *streamConnect) StreamScopes(context.Context, string) ([]contract.Scope, bool, error) {
	if s.read.Load() {
		return []contract.Scope{contract.ScopeRead}, true, nil
	}
	return nil, true, nil
}

func TestWebConnectStreamRechecksScope(t *testing.T) {
	devices := &streamDeviceStore{}
	sessions := &streamConnect{}
	sessions.read.Store(true)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, devices)
	if err != nil {
		t.Fatal(err)
	}
	w.SetConnect(sessions)
	tick := make(chan time.Time)
	w.ticks = func() (<-chan time.Time, func()) { return tick, func() {} }
	started, ended := make(chan struct{}), make(chan struct{})
	w.handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	r := httptest.NewRequest(http.MethodGet, "http://dyna.localhost:7790/v1/events", nil)
	go func() { w.ServeHTTP(httptest.NewRecorder(), r); close(ended) }()
	<-started
	sessions.read.Store(false)
	tick <- time.Time{}
	<-ended
}

func TestWebRemoteWebSocketRejectsWrongOrigin(t *testing.T) {
	devices := &streamDeviceStore{}
	devices.live.Store(true)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, devices)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetRoutes([]string{"https://dyna.example.com"}); err != nil {
		t.Fatal(err)
	}
	w.handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for origin, want := range map[string]int{"https://other.example.com": http.StatusForbidden, "https://dyna.example.com": http.StatusNoContent} {
		r := httptest.NewRequest(http.MethodGet, "https://dyna.example.com/v1/terminal", nil)
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Origin", origin)
		r.AddCookie(&http.Cookie{Name: "dyna_device", Value: "token"})
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Fatalf("origin %s: got %d, want %d", origin, rec.Code, want)
		}
	}
}

func TestWebPairUsesRemoteDefaultScopes(t *testing.T) {
	now := time.Unix(1000, 0)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	code, err := st.NewPairCode(context.Background(), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, func() time.Time { return now }, st)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetRoutes([]string{"https://dyna.example.com"}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://dyna.example.com/v1/pair", strings.NewReader(`{"code":"`+code+`","name":"Phone"}`))
	r.Header.Set("Origin", "https://dyna.example.com")
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusOK || len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("pair: %d, cookies %+v", rec.Code, cookies)
	}
	_, valid, err := st.AuthenticateDevice(context.Background(), sha256.Sum256([]byte(cookies[0].Value)), now)
	if err != nil || !valid {
		t.Fatalf("paired device authentication: %v %v", valid, err)
	}
	r = httptest.NewRequest(http.MethodPost, "https://dyna.example.com/v1/host.add", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://dyna.example.com")
	r.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("default scopes allow admin: %d", rec.Code)
	}
}

func TestPairAnswersGoneWithNoLiveCode(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, st)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "http://dyna.localhost:7790/v1/pair", strings.NewReader(`{"code":"ABCDE23456","name":"Phone"}`))
	r.Header.Set("Origin", "http://dyna.localhost:7790")
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, r)
	if rec.Code != http.StatusGone {
		t.Fatalf("pair with no live code: %d", rec.Code)
	}
}

func TestWebDiscoversAndReplacesRoutesOnMiss(t *testing.T) {
	now := time.Unix(1000, 0)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetRoutes([]string{"https://configured.example"}); err != nil {
		t.Fatal(err)
	}
	route, calls, wakes := "https://first.example", 0, 0
	w.SetDiscovery(func(context.Context, int) (string, error) { calls++; return route, nil }, func() { wakes++ })
	request := func(host string) int {
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil))
		return rec.Code
	}
	if got := request("first.example"); got != 200 || calls != 1 || wakes != 1 {
		t.Fatalf("first: %d, calls %d, wakes %d", got, calls, wakes)
	}
	route = "https://second.example"
	if got := request("second.example"); got != 421 || calls != 1 {
		t.Fatalf("throttle: %d, calls %d", got, calls)
	}
	now = now.Add(30 * time.Second)
	if got := request("second.example"); got != 200 || calls != 2 || wakes != 2 {
		t.Fatalf("changed: %d, calls %d, wakes %d", got, calls, wakes)
	}
	if got := request("first.example"); got != 421 {
		t.Fatalf("old host: %d", got)
	}
	if got := request("configured.example"); got != 200 {
		t.Fatalf("configured host: %d", got)
	}
	route = ""
	now = now.Add(30 * time.Second)
	if got := request("missing.example"); got != 421 || request("second.example") != 421 || wakes != 3 {
		t.Fatalf("removed: %d, wakes %d", got, wakes)
	}
}

func TestDiscoveryFailureKeepsRouteUntilSuccessfulRemoval(t *testing.T) {
	now := time.Unix(1000, 0)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	route, failure := "https://host.example", error(nil)
	w.SetDiscovery(func(context.Context, int) (string, error) { return route, failure }, nil)
	w.RefreshRoutes(context.Background())
	now = now.Add(30 * time.Second)
	failure = errors.New("unavailable")
	w.RefreshRoutes(context.Background())
	if !slices.Equal(w.PublicRoutes(), []string{"https://host.example"}) {
		t.Fatalf("lost route: %v", w.PublicRoutes())
	}
	now = now.Add(30 * time.Second)
	failure, route = nil, ""
	w.RefreshRoutes(context.Background())
	if len(w.PublicRoutes()) != 0 {
		t.Fatalf("route not removed: %v", w.PublicRoutes())
	}
}

func TestTunnelRefreshDoesNotWakeTunnel(t *testing.T) {
	now := time.Unix(1000, 0)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetRoutes([]string{"https://configured.example"}); err != nil {
		t.Fatal(err)
	}
	route, wakes := "https://configured.example", 0
	w.SetDiscovery(func(context.Context, int) (string, error) { return route, nil }, func() { wakes++ })
	w.RefreshRoutes(context.Background())
	route = "https://new.example"
	now = now.Add(30 * time.Second)
	w.RefreshRoutes(context.Background())
	if wakes != 0 || !slices.Equal(w.PublicRoutes(), []string{"https://configured.example", "https://new.example"}) {
		t.Fatalf("wakes %d, routes %v", wakes, w.PublicRoutes())
	}
}

func TestWebDiscoverySharesInFlightResult(t *testing.T) {
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}, func() time.Time { return time.Unix(1000, 0) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	w.SetDiscovery(func(context.Context, int) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return "https://first.example", nil
	}, nil)
	results := make(chan int, 2)
	request := func() {
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://first.example/", nil))
		results <- rec.Code
	}
	go request()
	<-started
	go request()
	close(release)
	if a, b := <-results, <-results; a != 200 || b != 200 || calls.Load() != 1 {
		t.Fatalf("responses %d, %d; discoveries %d", a, b, calls.Load())
	}
}

func TestPublicRoutesLeaveOutLoopback(t *testing.T) {
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetRoutes([]string{"https://bad.example;script-src"}); err == nil {
		t.Fatal("accepted invalid configured origin")
	}
	if err := w.SetRoutes([]string{"https://b.example.com", "https://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if got := w.PublicRoutes(); !slices.Equal(got, []string{"https://a.example.com", "https://b.example.com"}) {
		t.Fatalf("routes: %v", got)
	}
	rec := httptest.NewRecorder()
	w.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://a.example.com/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "https://a.example.com") || !strings.Contains(csp, "https://b.example.com") {
		t.Fatalf("configured routes missing from CSP: %s", csp)
	}
}

func TestWebGuardAndLogin(t *testing.T) {
	now := time.Unix(1000, 0)
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	a := New(&fakeService{}, nil)
	w, err := NewWeb(a, "127.0.0.1:7790", fstest.MapFS{"index.html": {Data: []byte("app")}}, func() time.Time { return now }, st)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, host, path, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader("{}"))
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, r)
		return rec
	}
	for _, host := range []string{"127.0.0.1:7790", "localhost:7790"} {
		if got := request("GET", host, "/", "", nil).Code; got != http.StatusMisdirectedRequest {
			t.Fatalf("host %s: %d", host, got)
		}
	}
	host := "dyna.localhost:7790"
	if got := request("POST", host, "/v1/post", "http://evil", nil).Code; got != 403 {
		t.Fatal(got)
	}
	if got := request("GET", host, "/v1/events", "", nil).Code; got != 403 {
		t.Fatal(got)
	}
	login, problem := w.login(nil, contract.Principal{}, contract.WebLoginRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	u, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := request("GET", host, u.RequestURI(), "", nil)
	if rec.Code != 303 || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", rec.Code)
	}
	cookie := rec.Result().Cookies()[0]
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	w.devices = st
	if !cookie.HttpOnly || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", cookie)
	}
	if got := request("GET", host, u.RequestURI(), "", nil).Code; got != 403 {
		t.Fatal(got)
	}
	if got := request("POST", host, "/v1/room.list", "http://"+host, cookie).Code; got != 200 {
		t.Fatal(got)
	}
	if got := request("POST", host, "/v1/web.login", "http://"+host, cookie).Code; got != http.StatusForbidden {
		t.Fatal(got)
	}
	expiring, _ := w.login(nil, contract.Principal{}, contract.WebLoginRequest{})
	u, _ = url.Parse(expiring.URL)
	now = now.Add(time.Minute)
	if got := request("GET", host, u.RequestURI(), "", nil).Code; got != 403 {
		t.Fatal(got)
	}
}

func TestWebReusesMuxWithCurrentScopes(t *testing.T) {
	devices := &streamDeviceStore{}
	devices.live.Store(true)
	w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, devices)
	if err != nil {
		t.Fatal(err)
	}
	w.ticks = func() (<-chan time.Time, func()) {
		t.Fatal("non-stream request started a ticker")
		return nil, func() {}
	}
	for _, read := range []bool{true, false} {
		devices.read.Store(read)
		r := httptest.NewRequest(http.MethodPost, "http://dyna.localhost:7790/v1/device.scopes", strings.NewReader("{}"))
		r.AddCookie(&http.Cookie{Name: "dyna_device", Value: "token"})
		out := httptest.NewRecorder()
		w.ServeHTTP(out, r)
		if out.Code != http.StatusOK || strings.Contains(out.Body.String(), `"read"`) != read {
			t.Fatalf("read=%v: %d %s", read, out.Code, out.Body.String())
		}
	}
}

func BenchmarkWebMux(b *testing.B) {
	a := New(&fakeService{}, nil)
	c := Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true}
	b.Run("build", func(b *testing.B) {
		for b.Loop() {
			_ = a.handler(c)
		}
	})
	b.Run("reuse", func(b *testing.B) {
		h := a.handler(c)
		b.ResetTimer()
		for b.Loop() {
			_ = h
		}
	})
}
