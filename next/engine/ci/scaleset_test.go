package ci

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

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
