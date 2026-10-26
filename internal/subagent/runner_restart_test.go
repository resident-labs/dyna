package subagent

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
)

func restartWaitFixture(t *testing.T) *runnerWrites {
	t.Helper()
	m := &Manager{Engine: engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.Notices(nil))}
	w := m.runnerWrites("ses_restart", 7)
	t.Cleanup(func() { w.out.Close(context.Background()) })
	return w
}

// The fake engine stays unavailable for 90 virtual seconds. The production
// wait loop, context deadlines and backoff timers all use that virtual clock.
func TestRunnerInputWaitSurvivesNinetySecondRestart(t *testing.T) {
	for _, outage := range []string{"starting", "refused", "starting then refused"} {
		t.Run(outage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := restartWaitFixture(t)
				start := time.Now()
				input := InputClaim{Turn: 7, Cursor: 123, Depth: 2, ParentSession: "ses_parent"}
				calls := 0
				w.waitCall = func(ctx context.Context, method string, args []any, result any) error {
					calls++
					if method != "RunnerWaitInput" || len(args) != 1 || !reflect.DeepEqual(args[0], input) || callspec.RequestOf(ctx) != "" {
						t.Fatalf("wait changed its method, input or request identity: %s, %+v", method, args)
					}
					if time.Since(start) < 90*time.Second {
						if outage == "refused" || outage == "starting then refused" && time.Since(start) >= 45*time.Second {
							return engineclient.ErrNotRunning
						}
						return &engineclient.RemoteError{Status: http.StatusServiceUnavailable, Message: "starting"}
					}
					ready, ok := result.(*bool)
					if !ok {
						t.Fatalf("wait result type: %T", result)
					}
					*ready = true
					return nil
				}
				ctx := callspec.WithRequest(context.Background(), "must-not-be-replayed-as-a-write")
				ready, err := w.waitInput(ctx, input)
				if err != nil || !ready || time.Since(start) < 90*time.Second || calls < 2 {
					t.Fatalf("restart after %s and %d reads: ready %v, error %v", time.Since(start), calls, ready, err)
				}
				if callspec.Deadline("RunnerWaitInput") != time.Minute {
					t.Fatal("restart recovery changed the individual engine call deadline")
				}
			})
		})
	}
}

func TestRunnerInputRestartWaitStaysBoundedAndCancelable(t *testing.T) {
	for _, end := range []string{"five-minute bound", "cancel", "revoked"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := restartWaitFixture(t)
				start := time.Now()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				w.waitCall = func(context.Context, string, []any, any) error {
					switch {
					case end == "cancel" && time.Since(start) >= 75*time.Second:
						cancel()
					case end == "revoked" && time.Since(start) >= 90*time.Second:
						return &engineclient.RemoteError{Status: http.StatusForbidden, Message: "turn is no longer current"}
					}
					return &engineclient.RemoteError{Status: http.StatusServiceUnavailable, Message: "starting"}
				}
				ready, err := w.waitInput(ctx, InputClaim{Turn: 7})
				if ready || err == nil {
					t.Fatalf("outage ended with ready %v, error %v", ready, err)
				}
				switch end {
				case "five-minute bound":
					if time.Since(start) != 5*time.Minute || err.Error() != callspec.Late("RunnerWaitInput", 5*time.Minute).Error() {
						t.Fatalf("outage budget: %s, %v", time.Since(start), err)
					}
				case "cancel":
					if !errors.Is(err, context.Canceled) || time.Since(start) < 75*time.Second || time.Since(start) > 76*time.Second {
						t.Fatalf("cancel during restart: %s, %v", time.Since(start), err)
					}
				case "revoked":
					var remote *engineclient.RemoteError
					if !errors.As(err, &remote) || remote.Status != http.StatusForbidden || time.Since(start) < 90*time.Second {
						t.Fatalf("revoked after restart: %s, %v", time.Since(start), err)
					}
				}
			})
		})
	}
}

// An engine that stops under a running wait answers it with its own canceled
// context, a 4xx reply. The wait retries; the turn keeps its steering (D-267).
func TestRunnerInputWaitRetriesWhenTheEngineCancelsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := restartWaitFixture(t)
		calls := 0
		w.waitCall = func(_ context.Context, _ string, _ []any, result any) error {
			calls++
			if calls < 3 {
				return &engineclient.RemoteError{Status: http.StatusUnprocessableEntity, Message: context.Canceled.Error()}
			}
			ready, ok := result.(*bool)
			if !ok {
				t.Fatalf("wait result type: %T", result)
			}
			*ready = true
			return nil
		}
		ready, err := w.waitInput(context.Background(), InputClaim{Turn: 7})
		if err != nil || !ready || calls != 3 {
			t.Fatalf("after %d reads: ready %v, error %v", calls, ready, err)
		}
	})
}
