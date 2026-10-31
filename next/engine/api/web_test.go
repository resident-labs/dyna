package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type streamDeviceStore struct{ live atomic.Bool }

func (s *streamDeviceStore) CreateDevice(context.Context, store.Device) error { return nil }
func (s *streamDeviceStore) AuthenticateDevice(context.Context, [32]byte, time.Time) (store.Device, bool, error) {
	return store.Device{}, s.live.Load(), nil
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
	w.handler = a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true})
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
