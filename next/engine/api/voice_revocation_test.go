package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type voiceStreamAuth struct {
	streamDeviceStore
	streamConnect
	live    atomic.Bool
	operate atomic.Bool
}

func (s *voiceStreamAuth) AuthenticateDevice(ctx context.Context, _ [32]byte, _ time.Time) (store.Device, bool, error) {
	scopes, live, err := s.StreamScopes(ctx, "")
	return store.Device{ID: "device", Scopes: scopes}, live, err
}
func (s *voiceStreamAuth) Authenticate(ctx context.Context, _ *http.Request, _ string) (store.ConnectSession, []contract.Scope, bool, error) {
	scopes, live, err := s.StreamScopes(ctx, "")
	return store.ConnectSession{ID: "session", Device: "device"}, scopes, live, err
}
func (s *voiceStreamAuth) StreamScopes(context.Context, string) ([]contract.Scope, bool, error) {
	scopes := []contract.Scope{contract.ScopeRead}
	if s.operate.Load() {
		scopes = append(scopes, contract.ScopeOperate)
	}
	return scopes, s.live.Load(), nil
}

func TestWebVoiceStreamRevocation(t *testing.T) {
	for _, connect := range []bool{false, true} {
		for _, removeScope := range []bool{false, true} {
			name := map[bool]string{false: "device", true: "connect"}[connect] + "/" + map[bool]string{false: "revoked", true: "operate removed"}[removeScope]
			t.Run(name, func(t *testing.T) {
				auth := &voiceStreamAuth{}
				auth.live.Store(true)
				auth.operate.Store(true)
				devices := &streamDeviceStore{}
				var deviceStore DeviceStore = auth
				if connect {
					deviceStore = devices
				}
				w, err := NewWeb(New(&fakeService{}, nil), "127.0.0.1:7790", fstest.MapFS{}, time.Now, deviceStore)
				if err != nil {
					t.Fatal(err)
				}
				if connect {
					w.SetConnect(auth)
				}
				ticks := make(chan time.Time, 1)
				w.ticks = func() (<-chan time.Time, func()) { return ticks, func() {} }
				started, ended := make(chan struct{}), make(chan struct{})
				w.handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := httptest.NewRequest(http.MethodGet, "http://dyna.localhost:7790/v1/voice", nil).WithContext(ctx)
				if !connect {
					r.AddCookie(&http.Cookie{Name: "dyna_device", Value: "token"})
				}
				go func() { w.ServeHTTP(httptest.NewRecorder(), r); close(ended) }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("voice stream not started")
				}
				if removeScope {
					auth.operate.Store(false)
				} else {
					auth.live.Store(false)
				}
				ticks <- time.Time{}
				select {
				case <-ended:
				case <-time.After(5 * time.Second):
					t.Fatal("voice stream not cancelled")
				}
			})
		}
	}
}
