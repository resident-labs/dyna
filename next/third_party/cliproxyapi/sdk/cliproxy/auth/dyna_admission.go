package auth

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type dynaAdmissionDeadlineKey struct{}

const (
	dynaMaxAdmissionCooldown = 5 * time.Minute
	dynaMaxAdmissionJitter   = 30 * time.Second
)

// One deadline bounds both selection and retry waits, not execution or streaming.
func withDynaAdmissionDeadline(ctx context.Context) context.Context {
	if ctx.Value(dynaAdmissionDeadlineKey{}) != nil || ctx.Value(dynaEligibleContextKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, dynaAdmissionDeadlineKey{}, time.Now().Add(dynaMaxAdmissionCooldown+dynaMaxAdmissionJitter))
}

func dynaRetryWaitLimit(ctx context.Context, maxWait time.Duration) time.Duration {
	if deadline, ok := ctx.Value(dynaAdmissionDeadlineKey{}).(time.Time); ok {
		return min(maxWait, max(0, time.Until(deadline)))
	}
	return maxWait
}

// Dyna holds short selection cooldowns instead of spending the caller's retry
// budget. No executor has run when selection reports cooldown, so retrying here
// cannot duplicate a generation. Non-Dyna callers retain the upstream policy.
func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	ctx = withDynaAdmissionDeadline(ctx)
	deadline, ok := ctx.Value(dynaAdmissionDeadlineKey{}).(time.Time)
	if !ok {
		return m.pickNextMixedRaw(ctx, providers, model, opts, tried)
	}
	for {
		a, executor, provider, err := m.pickNextMixedRaw(ctx, providers, model, opts, tried)
		if err == nil {
			return a, executor, provider, nil
		}
		var cooldown *modelCooldownError
		if !errors.As(err, &cooldown) || cooldown.resetIn <= 0 || cooldown.resetIn > dynaMaxAdmissionCooldown {
			return nil, nil, "", err
		}
		jitter := min(max(cooldown.resetIn/10, 250*time.Millisecond), dynaMaxAdmissionJitter)
		delay := cooldown.resetIn + time.Duration(rand.Int64N(int64(jitter)))
		if time.Now().Add(delay).After(deadline) {
			return nil, nil, "", err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, "", ctx.Err()
		case <-timer.C:
		}
	}
}
