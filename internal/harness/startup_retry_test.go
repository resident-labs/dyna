package harness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStartupNotReadyRetry(t *testing.T) {
	started := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		now       time.Time
		second    error
		wantCalls int
		wantWait  time.Duration
		wantError bool
	}{
		{"transient", started.Add(time.Second), nil, 2, 29 * time.Second, false},
		{"repeated", started.Add(time.Second), errors.New("runtime is not ready: signed out"), 2, 29 * time.Second, true},
		{"after startup", started.Add(time.Minute), nil, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var waited time.Duration
			_, err := retryStartupNotReady(context.Background(), func() (Result, error) {
				calls++
				if calls == 1 {
					return Result{}, errors.New("runtime is not ready: signed out")
				}
				return Result{}, tc.second
			}, func(context.Context) (time.Time, error) { return started, nil }, func() time.Time { return tc.now }, func(_ context.Context, d time.Duration) error { waited = d; return nil })
			if calls != tc.wantCalls || waited != tc.wantWait || (err != nil) != tc.wantError {
				t.Fatalf("calls=%d wait=%v error=%v", calls, waited, err)
			}
		})
	}
}
