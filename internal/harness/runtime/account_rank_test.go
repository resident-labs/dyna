package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

// roundTripFunc adapts a function to an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fixtureUsageEndpoint answers the Claude usage endpoint per access token.
// Tokens left out get a 503, which makes their probe fail: a probe failure
// fail-opens to the saved reading, exactly like a provider outage.
func fixtureUsageEndpoint(t *testing.T, responses map[string]string) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.anthropic.com" {
			return nil, http.ErrAbortHandler
		}
		body, ok := responses[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("no fixture")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
}

// usageBody builds a Claude usage response: percentages of the 5-hour and
// weekly caps, with the weekly reset at resetsAt.
func usageBody(session, weekly float64, resetsAt time.Time) string {
	return fmt.Sprintf(`{"five_hour":{"utilization":%g},"seven_day":{"utilization":%g,"resets_at":%q}}`, session, weekly, resetsAt.UTC().Format(time.RFC3339))
}

func setReading(t *testing.T, a *Accounts, n NativeAccount, o *store.Observation) {
	t.Helper()
	if err := a.transaction(func(s *store.State) error {
		if entry := s.Find(n.ID); entry != nil {
			entry.Observation = o
		}
		return nil
	}, true); err != nil {
		t.Fatal(err)
	}
}

func weeklyReading(used float64, hoursToReset float64, plan string) *store.Observation {
	session := 10.0
	return &store.Observation{
		Plan:       &store.Plan{ID: plan, Label: plan},
		ObservedAt: time.Now().Add(-time.Minute),
		Windows: []store.Window{
			{ID: "five_hour", Label: "Session (5-hour)", UsedPercent: &session, Unit: "percent", ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)},
			{ID: "seven_day", Label: "Weekly (7-day)", UsedPercent: &used, Unit: "percent", ResetsAt: time.Now().Add(time.Duration(hoursToReset * float64(time.Hour))).Format(time.RFC3339Nano)},
		},
	}
}

func savedObservation(t *testing.T, a *Accounts, id string) *store.Observation {
	t.Helper()
	var o *store.Observation
	if err := a.transaction(func(s *store.State) error {
		if entry := s.Find(id); entry != nil {
			o = entry.Observation
		}
		return nil
	}, false); err != nil {
		t.Fatal(err)
	}
	return o
}

// activate marks n the harness's active account, as the failed turn's own
// account would be.
func activate(t *testing.T, a *Accounts, n NativeAccount) {
	t.Helper()
	if err := a.transaction(func(*store.State) error { return a.setActive(n.Harness, n.ID) }, true); err != nil {
		t.Fatal(err)
	}
}

// An account whose weekly room resets in hours outranks one with more room
// that resets in days, whatever the labels say.
func TestFailoverRanksByTheUsageScore(t *testing.T) {
	a := testAccounts(t)
	failed := signedClaude(t, a, "failed", "aaaaaaaa-bbbb-cccc-dddd-00000000000f")
	later := signedClaude(t, a, "aaa-later", "aaaaaaaa-bbbb-cccc-dddd-00000000000a")
	soon := signedClaude(t, a, "zzz-soon", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	setReading(t, a, later, weeklyReading(10, 6*24, "max"))
	setReading(t, a, soon, weeklyReading(40, 14, "max_20x"))
	activate(t, a, failed)
	fixtureUsageEndpoint(t, map[string]string{
		"aaa-later-access": usageBody(10, 10, time.Now().Add(6*24*time.Hour)),
		"zzz-soon-access":  usageBody(30, 40, time.Now().Add(14*time.Hour)),
	})
	from, to, err := a.Failover(context.Background(), store.ProviderAnthropic, failed.ID, "", "/fake/native", time.Now().Add(time.Hour), true)
	if err != nil || from.ID != failed.ID || to.ID != soon.ID {
		t.Fatalf("from=%v to=%v err=%v; want the account that resets soon", from.ID, to.ID, err)
	}
}

// An account at 98% weekly use is never chosen, even with the first label.
func TestFailoverNeverPicksAnAccountNearItsCap(t *testing.T) {
	a := testAccounts(t)
	failed := signedClaude(t, a, "failed", "aaaaaaaa-bbbb-cccc-dddd-00000000000f")
	capped := signedClaude(t, a, "aaa-capped", "aaaaaaaa-bbbb-cccc-dddd-00000000000a")
	healthy := signedClaude(t, a, "zzz-healthy", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	setReading(t, a, capped, weeklyReading(98, 24, "max"))
	setReading(t, a, healthy, weeklyReading(20, 24, "max"))
	activate(t, a, failed)
	fixtureUsageEndpoint(t, map[string]string{
		"zzz-healthy-access": usageBody(15, 20, time.Now().Add(24*time.Hour)),
	})
	from, to, err := a.Failover(context.Background(), store.ProviderAnthropic, failed.ID, "", "/fake/native", time.Now().Add(time.Hour), true)
	if err != nil || from.ID != failed.ID || to.ID != healthy.ID {
		t.Fatalf("from=%v to=%v err=%v; want the healthy account", from.ID, to.ID, err)
	}
}

// A quota-blocked account (every window exhausted past now) is skipped and
// its reset is named in the error when nothing else is left.
func TestFailoverSkipsAQuotaBlockedAccount(t *testing.T) {
	a := testAccounts(t)
	failed := signedClaude(t, a, "failed", "aaaaaaaa-bbbb-cccc-dddd-00000000000f")
	blocked := signedClaude(t, a, "blocked", "aaaaaaaa-bbbb-cccc-dddd-00000000000b")
	used := 100.0
	reset := time.Now().Add(5 * time.Hour)
	setReading(t, a, blocked, &store.Observation{
		ObservedAt: time.Now().Add(-time.Minute),
		Windows: []store.Window{
			{ID: "five_hour", UsedPercent: &used, ResetsAt: reset.Format(time.RFC3339Nano)},
			{ID: "seven_day", UsedPercent: &used, ResetsAt: time.Now().Add(40 * time.Hour).Format(time.RFC3339Nano)},
		},
	})
	activate(t, a, failed)
	fixtureUsageEndpoint(t, nil)
	// The failed account's own limit ends after the blocked account's five-hour
	// reset, so the error names the sooner recovery.
	_, _, err := a.Failover(context.Background(), store.ProviderAnthropic, failed.ID, "", "/fake/native", time.Now().Add(6*time.Hour), true)
	if err == nil {
		t.Fatal("a quota-blocked account took the turn")
	}
	if want := reset.Local().Format("Mon 15:04"); !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want the blocked account's reset %s", err, want)
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID == blocked.ID {
		t.Fatal("the switch moved onto the blocked account")
	}
}

// The chosen account's reading is refreshed before it is accepted: a stale
// healthy reading loses to a live one that says the account is at its cap,
// and the fresh reading is recorded for the next choice.
func TestFailoverRefreshesTheChosenReadingFirst(t *testing.T) {
	a := testAccounts(t)
	failed := signedClaude(t, a, "failed", "aaaaaaaa-bbbb-cccc-dddd-00000000000f")
	stale := signedClaude(t, a, "aaa-stale", "aaaaaaaa-bbbb-cccc-dddd-00000000000a")
	steady := signedClaude(t, a, "zzz-steady", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	setReading(t, a, stale, weeklyReading(10, 48, "max_20x"))
	setReading(t, a, steady, weeklyReading(50, 24, "max"))
	activate(t, a, failed)
	fixtureUsageEndpoint(t, map[string]string{
		// The top-ranked account is near its cap on a fresh reading.
		"aaa-stale-access": usageBody(97, 98, time.Now().Add(24*time.Hour)),
		// The runner-up's endpoint is down; its saved reading stands.
	})
	from, to, err := a.Failover(context.Background(), store.ProviderAnthropic, failed.ID, "", "/fake/native", time.Now().Add(time.Hour), true)
	if err != nil || from.ID != failed.ID || to.ID != steady.ID {
		t.Fatalf("from=%v to=%v err=%v; want the steady account after the refresh", from.ID, to.ID, err)
	}
	if o := savedObservation(t, a, stale.ID); o == nil {
		t.Fatal("the refreshed near-cap reading was not recorded")
	} else if w := weeklyOf(o); w == nil || w.UsedPercent == nil || *w.UsedPercent != 98 {
		t.Fatalf("the refreshed near-cap reading was not recorded: %#v", w)
	}
}

func weeklyOf(o *store.Observation) *store.Window {
	for i := range o.Windows {
		if o.Windows[i].ID == "seven_day" {
			return &o.Windows[i]
		}
	}
	return nil
}
