package auth

import (
	"testing"
	"time"
)

func TestDynaQuotaRecoveryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Auth, *QuotaResetHint)
		recover bool
	}{
		{"fresh", func(*Auth, *QuotaResetHint) {}, true},
		{"older", func(a *Auth, h *QuotaResetHint) { h.ObservedAt = a.UpdatedAt.Add(-time.Second) }, false},
		{"equal", func(a *Auth, h *QuotaResetHint) { h.ObservedAt = a.UpdatedAt }, false},
		{"future", func(_ *Auth, h *QuotaResetHint) { h.ObservedAt = time.Now().Add(time.Hour) }, false},
		{"empty", func(_ *Auth, h *QuotaResetHint) { h.Windows = nil }, false},
		{"exhausted", func(_ *Auth, h *QuotaResetHint) { h.Windows[0].UsedPercent = 100 }, false},
		{"unauthorized", func(a *Auth, _ *QuotaResetHint) { a.LastError = &Error{HTTPStatus: 401} }, false},
		{"codex", func(a *Auth, _ *QuotaResetHint) { a.Provider = "codex" }, true},
		{"other-provider", func(a *Auth, _ *QuotaResetHint) { a.Provider = "antigravity" }, false},
		{"new-registration", func(a *Auth, _ *QuotaResetHint) { a.RegistrationEpoch++ }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			now := time.Now()
			a := &Auth{ID: "recovery", Provider: "claude", RegistrationEpoch: 1, Status: StatusError, Unavailable: true, UpdatedAt: now.Add(-time.Hour), Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(24 * time.Hour)}}
			reader := a.Clone()
			h := QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Seconds: 604800, UsedPercent: 75, ResetAt: now.Add(24 * time.Hour)}}}
			shadow := &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute), LastError: &Error{HTTPStatus: 503}, cooldownRecordedSeq: 42}
			a.ModelStates = map[string]*ModelState{"wrapped": {Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "credential_quota"}, credentialQuotaPropagated: true, credentialQuotaShadow: shadow}, "independent": {Unavailable: true, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: a.Quota.NextRecoverAt}}}
			tc.mutate(a, &h)
			manager.auths[a.ID] = a
			manager.PublishQuotaReadingForAuth(reader, h)
			if got := a.Quota.Reason != "credential_quota"; got != tc.recover {
				t.Fatalf("recovered=%v, want %v", got, tc.recover)
			}
			if tc.recover {
				if a.ModelStates["wrapped"].cooldownRecordedSeq != 42 || a.ModelStates["wrapped"].LastError.HTTPStatus != 503 {
					t.Fatal("lost independent shadow")
				}
				// Codex limits are account-wide, so its unscoped window releases a quota layer at the same deadline.
				if a.ModelStates["independent"].Quota.Exceeded == (a.Provider == "codex") {
					t.Fatal("independent quota layer handled wrongly for " + a.Provider)
				}
				// A replay cannot clear a subsequent credential rejection.
				a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota"}
				a.UpdatedAt = time.Now()
				manager.PublishQuotaReadingForAuth(reader, h)
				if !a.Quota.Exceeded {
					t.Fatal("replay cleared fresh rejection")
				}
			}
		})
	}
}

// Model 2026-11-21: after a Codex reset, a first release freed the sibling models, but the reporting model kept its own
// quota layer, and the credential read "quota" aggregated from it. A Codex read has only unscoped windows; they release it.
// Claude in the same shape keeps the layer: its unscoped weekly window does not prove a model's own limit.
func TestDynaCodexReportingModelRecovers(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			now := time.Now()
			deadline := now.Add(120 * time.Hour)
			a := &Auth{ID: "reporting", Provider: provider, RegistrationEpoch: 1, Status: StatusActive, UpdatedAt: now.Add(-time.Minute), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline}}
			a.ModelStates = map[string]*ModelState{
				"gpt-6.2-sol": {Status: StatusError, Unavailable: true, UpdatedAt: now.Add(-20 * time.Minute), NextRetryAfter: deadline, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline}},
				"gpt-6-luna":  {Status: StatusActive},
			}
			reader := a.Clone()
			manager.auths[a.ID] = a
			manager.PublishQuotaReadingForAuth(reader, QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Seconds: 604800, UsedPercent: 0, ResetAt: now.Add(168 * time.Hour)}}})
			released := !a.ModelStates["gpt-6.2-sol"].Quota.Exceeded && !a.Quota.Exceeded
			if released != (provider == "codex") {
				t.Fatalf("released=%v for %s", released, provider)
			}
		})
	}
}

// Model 2026-11-26: figueredoaria was reset early. The credential layer was released, and claude-opus-5-5, which had
// reported the weekly rejection on 23 Nov, kept its own layer until the old deadline while sonnet and fable ran.
// A layer whose signals name the shared weekly window is released; one without them (a model's own limit) stays.
func TestDynaClaudeSharedWeeklyReportingModelRecovers(t *testing.T) {
	for _, shared := range []bool{true, false} {
		manager := NewManager(nil, nil, nil)
		now := time.Now()
		deadline := now.Add(60 * time.Hour)
		signals := map[string]string{"Anthropic-Ratelimit-Unified-7d_oi-Status": "rejected"}
		if shared {
			signals = map[string]string{"Anthropic-Ratelimit-Unified-7d-Status": "rejected"}
		}
		a := &Auth{ID: "reporting", Provider: "claude", RegistrationEpoch: 1, Status: StatusActive, UpdatedAt: now.Add(-time.Minute), Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline}}
		a.ModelStates = map[string]*ModelState{
			"claude-opus-5-5":   {Status: StatusError, Unavailable: true, UpdatedAt: now.Add(-72 * time.Hour), NextRetryAfter: deadline, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline, Signals: signals}},
			"claude-sonnet-5-5": {Status: StatusActive},
		}
		reader := a.Clone()
		manager.auths[a.ID] = a
		manager.PublishQuotaReadingForAuth(reader, QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{{Seconds: 604800, UsedPercent: 2, ResetAt: deadline}}})
		opus := a.ModelStates["claude-opus-5-5"]
		if released := !opus.Quota.Exceeded && !opus.Unavailable && !a.Quota.Exceeded; released != shared {
			t.Fatalf("released=%v with shared=%v", released, shared)
		}
	}
}
