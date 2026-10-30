package harness

import (
	"context"
	"time"

	"dyna-agent/internal/engineclient"
)

// startupReadinessWindow gives account initialization time to finish before
// treating a runtime readiness error as a persistent profile failure.
const startupReadinessWindow = 30 * time.Second

func engineStarted(ctx context.Context) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var health struct {
		Started time.Time `json:"started"`
	}
	err := engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil)).Call(ctx, "Health", nil, &health)
	return health.Started, err
}

func waitStartup(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryStartupNotReady retries only a readiness failure observed during this
// engine's startup window. A repeated failure returns normally and keeps the
// existing profile hold. The callbacks let tests control time and readiness.
func retryStartupNotReady(ctx context.Context, run func() (Result, error), started func(context.Context) (time.Time, error), now func() time.Time, wait func(context.Context, time.Duration) error) (Result, error) {
	result, err := run()
	if err == nil || !IsNotReady(err.Error()) || ctx.Err() != nil {
		return result, err
	}
	start, healthErr := started(ctx)
	current := now()
	end := start.Add(startupReadinessWindow)
	if healthErr != nil || start.IsZero() || current.Before(start) || !current.Before(end) {
		return result, err
	}
	if waitErr := wait(ctx, end.Sub(current)); waitErr != nil {
		return Result{}, waitErr
	}
	return run()
}
