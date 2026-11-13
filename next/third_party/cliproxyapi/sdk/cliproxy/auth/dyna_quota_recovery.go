package auth

import (
	"context"
	"math"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Dyna: a fresh usage read can disprove an older credential-wide rejection.
// Do not use successful inference alone: it may be an in-flight request or a
// model outside the exhausted window. Independent model failures stay intact.
func (m *Manager) reconcileClaudeQuotaReading(id string, hint QuotaResetHint) {
	now := time.Now()
	if !hint.current(now) || hint.Prepaid || hint.Empty {
		return
	}
	hasWeekly := false
	for _, w := range hint.Windows {
		if math.IsNaN(w.UsedPercent) || math.IsInf(w.UsedPercent, 0) || w.UsedPercent < 0 || w.UsedPercent >= 100 {
			return
		}
		if w.Seconds >= soonestResetLongSeconds && w.Model == "" {
			hasWeekly = true
		}
	}
	if !hasWeekly {
		return
	}
	release := m.lockAuthMutation(id)
	defer release()
	m.mu.Lock()
	a := m.auths[id]
	if a == nil || a.Provider != "claude" || a.RegistrationEpoch != hint.registrationEpoch ||
		a.Quota.Reason != "credential_quota" || !a.Quota.Exceeded || !hint.ObservedAt.After(a.UpdatedAt) || hasUnauthorizedAuthFailure(a) {
		m.mu.Unlock()
		return
	}
	deadline := a.Quota.NextRecoverAt
	applyCooldownFields(&a.Quota, QuotaState{})
	a.NextRetryAfter = time.Time{}
	a.Unavailable = false
	a.LastError = nil
	a.StatusMessage = ""
	a.Status = StatusActive
	for model, state := range a.ModelStates {
		if state == nil {
			continue
		}
		if state.credentialQuotaPropagated || state.Quota.Reason == "credential_quota" {
			if shadow := state.credentialQuotaShadow; shadow != nil {
				*state = *shadow.cloneShallow()
			} else {
				resetModelState(state, now)
			}
		} else if state.Quota.Exceeded && state.Quota.Reason == "quota" && state.Quota.NextRecoverAt.Equal(deadline) && hint.ObservedAt.After(state.UpdatedAt) {
			// Legacy persisted reporting models lack the runtime propagation marker.
			// Require explicit model headroom before releasing that original quota layer.
			for _, window := range hint.Windows {
				if window.Model != "" && quotaWindowApplies(&window, model) {
					resetModelState(state, now)
					break
				}
			}
		}
	}
	updateAggregatedAvailability(a, now)
	a.Generation++
	a.UpdatedAt = now
	snapshot := a.Clone()
	_ = m.persistLocked(context.Background(), a)
	m.mu.Unlock()
	release()
	models, epoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(id)
	projections := make([]registry.ClientModelProjection, 0, len(models))
	for _, model := range models {
		if model != nil {
			projections = append(projections, m.clientModelProjectionForAuth(snapshot, model.ID, now))
		}
	}
	registry.GetGlobalRegistry().ApplyClientModelProjections(id, epoch, snapshot.Generation, projections)
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	m.persistCooldownStates(context.Background())
}
