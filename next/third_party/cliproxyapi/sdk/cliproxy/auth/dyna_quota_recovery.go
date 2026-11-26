package auth

import (
	"context"
	"math"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Dyna: a fresh usage read can disprove an older credential-wide rejection, for Claude and Codex, whose
// limits an owner can reset before the rejection's own deadline.
// Do not use successful inference alone: it may be an in-flight request or a
// model outside the exhausted window. Independent model failures stay intact.
func (m *Manager) reconcileQuotaReading(id string, hint QuotaResetHint) {
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
	// Codex limits are account-wide (its usage read has no per-model windows), so the reporting model's own
	// quota layer is the same rejection, and an unscoped window with headroom releases it. That layer can also
	// be all that is left after an earlier release, when the credential reads "quota" aggregated from it.
	codex := a != nil && a.Provider == "codex"
	// A Claude model that recorded the shared weekly window as rejected holds that same rejection, not a limit
	// of its own, so the unscoped weekly window releases it too. Model 2026-11-26: an early weekly reset freed
	// the account, and claude-opus-5-5, which had reported the rejection, stayed cooled down for three more days.
	sharedWeekly := func(state *ModelState) bool {
		return codex || state.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] == "rejected"
	}
	aggregated := codex && a.Quota.Reason == "quota"
	if a != nil && !aggregated && a.Quota.Reason == "quota" {
		for _, state := range a.ModelStates {
			if state != nil && state.Quota.Exceeded && state.Quota.Reason == "quota" && state.Quota.NextRecoverAt.Equal(a.Quota.NextRecoverAt) && sharedWeekly(state) {
				aggregated = true
				break
			}
		}
	}
	if a == nil || (a.Provider != "claude" && !codex) || a.RegistrationEpoch != hint.registrationEpoch ||
		!(a.Quota.Reason == "credential_quota" || aggregated) || !a.Quota.Exceeded || !hint.ObservedAt.After(a.UpdatedAt) || hasUnauthorizedAuthFailure(a) {
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
				if (window.Model != "" || sharedWeekly(state)) && quotaWindowApplies(&window, model) {
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
