package runtime

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

// A new sign-in clears the account's last renewal failure and its expired
// usage state, so the account stops showing the old failure.
func TestSignInClearsLastRenewalFailureAndExpiredState(t *testing.T) {
	accounts := testAccounts(t)
	account, err := accounts.Create("claude", "work")
	if err != nil {
		t.Fatal(err)
	}
	writeAccountFixture(t, account)
	account, err = accounts.Capture(context.Background(), account.ID, "/fake/native")
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{Root: accounts.Root}
	if err := s.RecordRenewal(t.Context(), store.Renewal{
		AccountID: account.ID, Harness: account.Harness, Provider: account.Provider,
		AttemptedAt: time.Now().Add(-time.Hour), Result: store.RenewalResultInvalidGrant, Reason: "invalid_grant",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *store.State) error {
		e := st.Find(account.ID)
		e.NeedsLogin = true
		e.Observation = &store.Observation{Reason: store.UsageReasonExpired, UsageError: "expired", Unavailable: true, Failures: 3}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := accounts.Capture(context.Background(), account.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestRenewals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got := latest[account.ID].Result; got != store.RenewalResultRenewed {
		t.Fatalf("last renewal = %q after sign-in, want %q", got, store.RenewalResultRenewed)
	}
	state, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	e := state.Find(account.ID)
	if e.NeedsLogin || e.Observation == nil || e.Observation.Reason != "" || e.Observation.UsageError != "" || e.Observation.Unavailable || e.Observation.Failures != 0 {
		t.Fatalf("sign-in kept the expired state: needsLogin=%v observation=%+v", e.NeedsLogin, e.Observation)
	}
}
