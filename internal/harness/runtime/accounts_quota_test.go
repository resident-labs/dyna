package runtime

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

func TestAccountQuotaAvailability(t *testing.T) {
	a := testAccounts(t)
	now := time.Now()
	check := func(want QuotaAvailability) {
		t.Helper()
		got, err := a.QuotaAvailability("claude-code", store.ProviderAnthropic, "repo", now)
		if err != nil || got.State != want {
			t.Fatalf("availability = %q, error %v; want %q", got, err, want)
		}
	}
	check(QuotaUnknown)
	n := signedClaude(t, a, "account", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	check(QuotaUnknown)
	used := 10.0
	setReading(t, a, n, &store.Observation{ObservedAt: now, Windows: []store.Window{{ID: "seven_day", UsedPercent: &used}}})
	check(QuotaAvailable)
	setReading(t, a, n, &store.Observation{ObservedAt: now.Add(-store.SavedUsageWindow - time.Second), Windows: []store.Window{{ID: "seven_day", UsedPercent: &used}}})
	check(QuotaUnknown)
	scopes, err := a.scopes()
	if err != nil {
		t.Fatal(err)
	}
	if err := scopes.MarkLimited(n.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(QuotaUnavailable)
}

// D-110: a quota-exhausted account keeps its harness out of automatic profile
// ranking, because the saved limit outlasts the turn's own failure backoff.
func TestQuotaHeldOut(t *testing.T) {
	a := testAccounts(t)
	sooner := signedClaude(t, a, "sooner", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	later := signedClaude(t, a, "later", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	now := time.Now().Truncate(time.Second)
	// With no limit saved, nothing is held out.
	if until, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now); held || !until.IsZero() {
		t.Fatalf("held = %v until %v with no limits saved", held, until)
	}
	// A harness with no account is never held out by quota alone.
	if _, held := a.QuotaHeldOut("kimi-code", store.ProviderKimiCode, "github.com/owner/repo", now); held {
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
	if _, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now); held {
		t.Fatal("a harness with a healthy account was held out")
	}
	if err = scopes.MarkLimited(later.ID, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Every account limited: held out until the first one resets.
	until, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now)
	if !held || !until.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("held = %v until %v, want the sooner reset", held, until)
	}
	// The first reset frees the harness even though the other stays limited.
	if _, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now.Add(31*time.Minute)); held {
		t.Fatal("the harness stayed out after its first account reset")
	}
	// An unknown provider for the harness answers nothing.
	if _, held := a.QuotaHeldOut("claude-code", "", "github.com/owner/repo", now); held {
		t.Fatal("an empty provider was held out")
	}
}

// D-110: a healthy account the repo rules refuse must not hide the quota hold
// of the account that may run there, or auto keeps choosing the exhausted
// profile and the turn fails on the same rejection.
func TestQuotaHeldOutSkipsAccountsTheRepoRefuses(t *testing.T) {
	a := testAccounts(t)
	allowed := signedClaude(t, a, "allowed", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	refused := signedClaude(t, a, "refused", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.SetRepos(refused.ID, []string{"github.com/other/repo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	scopes, err := a.scopes()
	if err != nil {
		t.Fatal(err)
	}
	if err = scopes.MarkLimited(allowed.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// The only account allowed in the repo is out of quota, so the harness is
	// held out even though a healthy account exists for other repos.
	until, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now)
	if !held || !until.Equal(now.Add(time.Hour)) {
		t.Fatalf("held = %v until %v, want the exhausted allowed account's reset", held, until)
	}
	// In the repo the healthy account serves, the harness stays pickable.
	if _, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/other/repo", now); held {
		t.Fatal("the healthy account's repo was held out")
	}
}

// A current usage reading with quota left releases a failure hold early. The
// z.ai coding plan reports a five-hour window but no weekly window.
func TestQuotaHeldOutReleasesLimitWhenUsageHasQuotaLeft(t *testing.T) {
	a := testAccounts(t)
	account := signedPiAccount(t, &Runtime{Accounts: a}, store.ProviderZAI, "fixture-key")
	scopes, err := a.scopes()
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Truncate(time.Second).Add(time.Hour)
	used := 26.0
	// This reading predates the failure and must not refute its hold.
	setReading(t, a, account, &store.Observation{ObservedAt: time.Now(), Windows: []store.Window{{ID: "five_hour", UsedPercent: &used}}})
	if err = scopes.MarkLimited(account.ID, until); err != nil {
		t.Fatal(err)
	}
	if got, held := a.QuotaHeldOut("pi", store.ProviderZAI, "github.com/owner/repo", time.Now()); !held || !got.Equal(until) {
		t.Fatalf("held = %v until %v with a pre-failure reading, want %v", held, got, until)
	}
	// A healthy reading captured after the failure releases the hold early.
	setReading(t, a, account, &store.Observation{ObservedAt: time.Now(), Windows: []store.Window{{ID: "five_hour", UsedPercent: &used}}})
	if _, held := a.QuotaHeldOut("pi", store.ProviderZAI, "github.com/owner/repo", time.Now()); held {
		t.Fatal("a post-failure usage reading with quota left did not release the hold")
	}
	used = 100
	setReading(t, a, account, &store.Observation{ObservedAt: time.Now(), Windows: []store.Window{{ID: "five_hour", UsedPercent: &used}}})
	if got, held := a.QuotaHeldOut("pi", store.ProviderZAI, "github.com/owner/repo", time.Now()); !held || !got.Equal(until) {
		t.Fatalf("held = %v until %v with a spent session window, want %v", held, got, until)
	}
}

// Profile ranking and account selection must apply the same eligibility
// decision when a reading cannot be ranked or has no weekly window.
func TestQuotaHeldOutAndChooseUsageShareEligibility(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	until := now.Add(time.Hour)
	for _, tc := range []struct {
		name     string
		reading  *store.Observation
		eligible bool
	}{
		{
			name:     "missing weekly percentage",
			reading:  &store.Observation{Windows: []store.Window{{ID: "seven_day"}}},
			eligible: true,
		},
		{
			name:     "active rate limit without weekly window",
			reading:  &store.Observation{RateLimitedUntil: &until},
			eligible: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAccounts(t)
			account := signedClaude(t, a, "account", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
			setReading(t, a, account, tc.reading)
			fixtureUsageEndpoint(t, nil)

			_, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "", now)
			chosen, _, _ := a.chooseUsage(context.Background(), []NativeAccount{account}, time.Time{}, "", now)
			if held == tc.eligible || (chosen != nil) != tc.eligible {
				t.Fatalf("held = %v, chosen = %v; want eligible = %v from both decisions", held, chosen != nil, tc.eligible)
			}
		})
	}
}

// A profile whose every account has no quota left in its saved usage reading
// is held out, even when no failover limit is stored. Service ranking reads
// this hold, so it must not start that profile and fail the turn.
func TestQuotaHeldOutSkipsSpentUsage(t *testing.T) {
	a := testAccounts(t)
	sooner := signedClaude(t, a, "sooner", "aaaaaaaa-bbbb-cccc-dddd-000000000011")
	later := signedClaude(t, a, "later", "aaaaaaaa-bbbb-cccc-dddd-000000000012")
	now := time.Now().Truncate(time.Second)
	used := 100.0
	setReading(t, a, sooner, &store.Observation{Windows: []store.Window{{ID: "seven_day", UsedPercent: &used, ResetsAt: now.Add(time.Hour).Format(time.RFC3339Nano)}}})
	setReading(t, a, later, &store.Observation{Windows: []store.Window{{ID: "seven_day", UsedPercent: &used, ResetsAt: now.Add(3 * time.Hour).Format(time.RFC3339Nano)}}})
	until, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now)
	if !held || !until.Equal(now.Add(time.Hour)) {
		t.Fatalf("held = %v until %v, want the sooner usage reset", held, until)
	}
	// One account with quota left keeps the harness pickable.
	open := 10.0
	setReading(t, a, later, &store.Observation{Windows: []store.Window{{ID: "seven_day", UsedPercent: &open, ResetsAt: now.Add(3 * time.Hour).Format(time.RFC3339Nano)}}})
	if _, held := a.QuotaHeldOut("claude-code", store.ProviderAnthropic, "github.com/owner/repo", now); held {
		t.Fatal("a harness with one account under the cap was held out")
	}
}

// D-500: a failed usage read keeps the windows of its last good reading. Its
// weekly reset is not an end of the failed read, so once the short rate limit
// has passed, the account is not held to that reset.
func TestFailedReadDoesNotHoldToItsWeeklyReset(t *testing.T) {
	now := time.Date(2026, 10, 29, 16, 10, 0, 0, time.UTC)
	used := 100.0
	weekly := []store.Window{{ID: "seven_day", UsedPercent: &used, ResetsAt: now.Add(6 * 24 * time.Hour).Format(time.RFC3339Nano)}}
	expired := now.Add(-time.Minute)
	if end, spent := readingOutOfQuota(&store.Observation{Unavailable: true, RateLimitedUntil: &expired, Windows: weekly}, now); spent {
		t.Fatalf("failed read held the account to %v after its rate limit ended", end)
	}
	live := now.Add(5 * time.Minute)
	if end, spent := readingOutOfQuota(&store.Observation{Unavailable: true, RateLimitedUntil: &live, Windows: weekly}, now); !spent || !end.Equal(live) {
		t.Fatalf("rate limit held = %v until %v, want until %v", spent, end, live)
	}
}
