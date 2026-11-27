package cliproxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type shutdownRefreshExecutor struct {
	serviceTestPluginExecutor
	started chan struct{}
}

func (e shutdownRefreshExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	close(e.started)
	// Model a provider returning rotated credentials despite cancellation.
	<-ctx.Done()
	auth.Metadata["refresh_token"] = "new-grant"
	return auth, nil
}

type shutdownRefreshStore struct {
	dynaTestStore
	started chan struct{}
	release chan struct{}
	saved   chan struct{}
}

func (*shutdownRefreshStore) RequireDurableAuth() bool { return true }

func (s *shutdownRefreshStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	close(s.started)
	<-s.release
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if auth.Metadata["refresh_token"] != "new-grant" {
		return "", errors.New("missing rotated refresh token")
	}
	close(s.saved)
	return auth.ID, nil
}

type shutdownRefreshHook struct {
	coreauth.NoopHook
	updated func()
}

func (h shutdownRefreshHook) OnAuthUpdated(context.Context, *coreauth.Auth) { h.updated() }

func awaitRefreshShutdown[T any](t *testing.T, deadline context.Context, what string, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-deadline.Done():
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func TestServiceShutdownDrainsScheduledRefresh(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout bool
	}{
		{name: "save before teardown with hook reentry"},
		{name: "deadline expires with save still pending", timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			watchdog, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			st := &shutdownRefreshStore{started: make(chan struct{}), release: make(chan struct{}), saved: make(chan struct{})}
			release := sync.OnceFunc(func() { close(st.release) })
			t.Cleanup(release)
			ex := shutdownRefreshExecutor{started: make(chan struct{})}
			var svc *Service
			hookDone := make(chan error, 1)
			manager := coreauth.NewManager(st, nil, shutdownRefreshHook{updated: func() {
				hookDone <- svc.Shutdown(watchdog)
			}})
			teardown := make(chan bool, 1)
			svc = &Service{coreManager: manager, runCancel: func() {
				select {
				case <-st.saved:
					teardown <- true
				default:
					teardown <- false
				}
			}}
			manager.RegisterExecutor(ex)
			if _, err := manager.Register(coreauth.WithSkipPersist(watchdog), &coreauth.Auth{
				ID: "scheduled-refresh", Provider: ex.Identifier(),
				Metadata: map[string]any{"refresh_token": "old-grant", "refresh_interval_seconds": 60},
			}); err != nil {
				t.Fatal(err)
			}
			manager.StartAutoRefresh(watchdog, time.Minute)
			defer manager.StopAutoRefresh()
			awaitRefreshShutdown(t, watchdog, "scheduled refresh", ex.started)

			stop := make(chan error, 1)
			go func() { stop <- svc.Shutdown(watchdog) }()
			awaitRefreshShutdown(t, watchdog, "save after stop cancels the loop", st.started)

			if tc.timeout {
				// Use the service's default grace period, not the outer watchdog.
				budget, stopBudget := context.WithTimeout(watchdog, 2*time.Second)
				defer stopBudget()
				if err := awaitRefreshShutdown(t, budget, "bounded shutdown", stop); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Shutdown with blocked save = %v, want deadline exceeded", err)
				}
				if awaitRefreshShutdown(t, watchdog, "teardown", teardown) {
					t.Fatal("save completed despite the gate held until the deadline")
				}
				release()
			} else {
				release()
				if err := awaitRefreshShutdown(t, watchdog, "shutdown after save", stop); err != nil {
					t.Fatal(err)
				}
				if !awaitRefreshShutdown(t, watchdog, "teardown", teardown) {
					t.Fatal("teardown started before the refresh grant was saved")
				}
			}
			if err := awaitRefreshShutdown(t, watchdog, "reentrant shutdown from the auth update hook", hookDone); err != nil {
				t.Fatalf("hook Shutdown = %v, want nil without waiting for itself", err)
			}
		})
	}
}
