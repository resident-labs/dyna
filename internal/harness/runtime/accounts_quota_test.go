package runtime

import (
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

// D-110: a quota-exhausted account keeps its harness out of automatic profile
// ranking, because the saved limit outlasts the turn's own failure backoff.
func TestQuotaHeldOut(t *testing.T) {
	a := testAccounts(t)
	sooner := signedClaude(t, a, "sooner", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	later := signedClaude(t, a, "later", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	now := time.Now().Truncate(time.Second)
	// With no limit saved, nothing is held out.
	if until, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, now); held || !until.IsZero() {
		t.Fatalf("held = %v until %v with no limits saved", held, until)
	}
	// A harness with no account is never held out by quota alone.
	if _, held := a.QuotaHeldOut("kimi-code", store.ProviderKimiCode, now); held {
		t.Fatal("a harness without accounts was held out")
	}
	scopes, err := a.scopes()
	if err != nil {
		t.Fatal(err)
	}
	if err = scopes.MarkLimited(sooner.ID, now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// One healthy account of the harness keeps it pickable.
	if _, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, now); held {
		t.Fatal("a harness with a healthy account was held out")
	}
	if err = scopes.MarkLimited(later.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Every account limited: held out until the first one resets.
	until, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, now)
	if !held || !until.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("held = %v until %v, want the sooner reset", held, until)
	}
	// The first reset frees the harness even though the other stays limited.
	if _, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, now.Add(31*time.Minute)); held {
		t.Fatal("the harness stayed out after its first account reset")
	}
	// An unknown provider for the harness answers nothing.
	if _, held := a.QuotaHeldOut("claude-code", "", now); held {
		t.Fatal("an empty provider was held out")
	}
}
