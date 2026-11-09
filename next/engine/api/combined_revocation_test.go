package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"
)

func TestCombinedStreamRevocationAndScopeRemoval(t *testing.T) {
	for _, scopeRemoval := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "scope removed"}[scopeRemoval], func(t *testing.T) {
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
			w.handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
			r := httptest.NewRequest("GET", "http://dyna.localhost:7790/v1/events?room=r&after=0&roomAfter=0", nil)
			r.AddCookie(&http.Cookie{Name: "dyna_device", Value: "token"})
			go func() { w.ServeHTTP(httptest.NewRecorder(), r); close(ended) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("stream not started")
			}
			if scopeRemoval {
				devices.read.Store(false)
			} else {
				devices.live.Store(false)
			}
			tick <- time.Time{}
			select {
			case <-ended:
			case <-time.After(5 * time.Second):
				t.Fatal("stream not revoked")
			}
		})
	}
}
