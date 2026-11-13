package auth

import (
	"testing"
	"time"
)

func TestDynaClaudeQuotaRecoveryEvidence(t *testing.T) {
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
		{"other-provider", func(a *Auth, _ *QuotaResetHint) { a.Provider = "codex" }, false},
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
				if !a.ModelStates["independent"].Quota.Exceeded {
					t.Fatal("cleared independent quota")
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
