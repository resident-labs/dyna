package ci

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSetsWithoutCredential(t *testing.T) {
	for _, method := range []string{"open", "Listen", "JIT", "Delete"} {
		t.Run(method, func(t *testing.T) {
			s := &Sets{}
			ctx := context.Background()
			var err error
			switch method {
			case "open":
				_, err = s.open(ctx, repo, "dyna-small")
			case "Listen":
				err = s.Listen(ctx, repo, "dyna-small", func(context.Context, int) int {
					t.Fatal("pool without a credential polled")
					return 0
				})
			case "JIT":
				var id int64
				var config string
				id, config, err = s.JIT(ctx, repo, "dyna-small", "runner")
				if id != 0 || config != "" {
					t.Fatalf("runner registered without a credential: %d, %q", id, config)
				}
			case "Delete":
				err = s.Delete(ctx, repo, "dyna-small")
			}
			if err == nil || err.Error() != "no GitHub credential" {
				t.Fatalf("error = %v, want no GitHub credential", err)
			}
			if len(s.sets) != 0 {
				t.Fatal("cached a scale-set client without a credential")
			}
		})
	}
}

// A nonempty invalid credential still takes the PAT path. Cancellation keeps
// this boundary test offline; it must reach HTTP, not nil GitHub App auth.
func TestSetsNonemptyCredentialUsesPATPath(t *testing.T) {
	for _, token := range []string{"not-a-real-token", " "} {
		t.Run(token, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			s := &Sets{Token: token}
			opened, err := s.open(ctx, repo, "dyna-small")
			if opened != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("open = %v, %v; want HTTP cancellation, not a local token-format check", opened, err)
			}
		})
	}
}

// This is a pool fixture with the real scale-set adapter, not an engine start.
func TestPoolWithoutCredentialStopsOnlyPool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		previous := log.Writer()
		log.SetOutput(&logs)
		defer log.SetOutput(previous)

		f := newFixture(0)
		f.store.hosts = nil
		sets := &Sets{}
		f.pool.Sets = sets
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ticks := make(chan time.Time, 2)
		done := make(chan struct{})
		go func() {
			defer close(done)
			f.pool.Run(ctx, ticks)
		}()
		// The initial tick starts listeners. Their missing-credential error is
		// collected on the next tick, as for any failed asynchronous listener.
		synctest.Wait()
		ticks <- time.Now()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("pool kept retrying without a credential")
		}
		if ctx.Err() != nil {
			t.Fatal("pool stopped its caller's context")
		}
		ticks <- time.Now()
		synctest.Wait()
		if got := strings.Count(logs.String(), "CI pool off: no GitHub credential"); got != 1 {
			t.Fatalf("off line count = %d, want 1; logs: %s", got, &logs)
		}
		runners, waiting := f.pool.Runners()
		if len(sets.sets) != 0 || len(f.api.leases) != 0 || len(runners) != 0 || len(waiting) != 0 {
			t.Fatalf("pool not off: clients=%d leases=%d runners=%d waiting=%d", len(sets.sets), len(f.api.leases), len(runners), len(waiting))
		}
	})
}

type closingSession struct {
	close func(context.Context) error
}

func (s closingSession) Close(ctx context.Context) error { return s.close(ctx) }

func TestMessageSessionCloseAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		closeMessageSession(ctx, closingSession{close: func(closeCtx context.Context) error {
			called = true
			if err := closeCtx.Err(); err != nil {
				t.Fatalf("session delete got a dead context: %v", err)
			}
			deadline, ok := closeCtx.Deadline()
			if !ok || time.Until(deadline) != 5*time.Second {
				t.Fatalf("session delete deadline %s (set=%v), want 5s", deadline, ok)
			}
			<-closeCtx.Done()
			if closeCtx.Err() != context.DeadlineExceeded {
				t.Fatalf("session delete ended with %v, want deadline", closeCtx.Err())
			}
			return closeCtx.Err()
		}})
		if !called {
			t.Fatal("session delete was not called")
		}
	})
}
