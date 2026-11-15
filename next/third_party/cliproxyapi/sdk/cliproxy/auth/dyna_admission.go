package auth

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type dynaAdmissionDeadlineKey struct{}

// One deadline follows the request through credential failover and retry rounds.
func withDynaAdmissionDeadline(ctx context.Context) context.Context {
	if _, ok := ctx.Value(dynaAdmissionDeadlineKey{}).(time.Time); ok {
		return ctx
	}
	return context.WithValue(ctx, dynaAdmissionDeadlineKey{}, time.Now().Add(60*time.Second+250*time.Millisecond))
}

// Dyna holds short selection cooldowns instead of spending the caller's retry
// budget. No executor has run when selection reports cooldown, so retrying here
// cannot duplicate a generation. Non-Dyna callers retain the upstream policy.
func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if authSelectionEligibilityForRequest(ctx, opts).allowedIDs == nil {
		return m.pickNextMixedRaw(ctx, providers, model, opts, tried)
	}
	ctx = withDynaAdmissionDeadline(ctx)
	deadline := ctx.Value(dynaAdmissionDeadlineKey{}).(time.Time)
	for {
		a, executor, provider, err := m.pickNextMixedRaw(ctx, providers, model, opts, tried)
		if err == nil {
			return a, executor, provider, nil
		}
		var cooldown *modelCooldownError
		if !errors.As(err, &cooldown) || cooldown.resetIn <= 0 || cooldown.resetIn > 60*time.Second {
			return nil, nil, "", err
		}
		delay := cooldown.resetIn + time.Duration(rand.Int64N(int64(250*time.Millisecond)))
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
