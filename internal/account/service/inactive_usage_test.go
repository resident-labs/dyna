package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

func TestInactiveUsageProbeDeadlines(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Minute)
	backoff := now.Add(time.Hour)
	for _, tc := range []struct {
		name string
		o    *store.Observation
		want time.Time
	}{
		{"never checked", nil, time.Time{}},
		{"interval", &store.Observation{ObservedAt: now}, now.Add(inactiveUsageWindow)},
		{"attempted", &store.Observation{ObservedAt: now.Add(-time.Hour), LastAttemptAt: now}, now.Add(inactiveUsageWindow)},
		{"reset", &store.Observation{ObservedAt: now, Windows: []Window{{ResetsAt: reset.Format(time.RFC3339)}}}, reset},
		{"reset already checked", &store.Observation{ObservedAt: now.Add(-time.Hour), LastAttemptAt: reset, Windows: []Window{{ResetsAt: reset.Format(time.RFC3339)}}}, reset.Add(inactiveUsageWindow)},
		{"fresh after reset", &store.Observation{ObservedAt: reset, Windows: []Window{{ResetsAt: reset.Format(time.RFC3339)}}}, reset.Add(inactiveUsageWindow)},
		{"provider backoff", &store.Observation{ObservedAt: now, RateLimitedUntil: &backoff, Windows: []Window{{ResetsAt: reset.Format(time.RFC3339)}}}, backoff},
		{"invalid reset", &store.Observation{ObservedAt: now, Windows: []Window{{ResetsAt: "invalid"}}}, now.Add(inactiveUsageWindow)},
		{"reading saved before typed reasons", &store.Observation{ObservedAt: now, LastAttemptAt: now, UsageError: "old generic result"}, time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := usageProbeAt(tc.o, false); !got.Equal(tc.want) {
				t.Fatalf("usageProbeAt = %v, want %v", got, tc.want)
			}
		})
	}
}

// D-148: an inactive reading is shown stale once it reaches the display window,
// so the timer must probe it again by then. A longer probe interval made an
// inactive account look stale for most of every cycle.
func TestInactiveProbeIsDueWhenTheReadingShowsStale(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{inactiveUsageWindow, inactiveUsageWindow + time.Minute, 25 * time.Minute} {
		if due := usageProbeAt(&store.Observation{ObservedAt: now.Add(-age)}, false); due.After(now) {
			t.Fatalf("a reading %s old is shown stale but is not due until %s", age, due.Sub(now))
		}
	}
	if got := savedTTL("claude-code"); got != inactiveUsageWindow {
		t.Fatalf("savedTTL = %s, want the inactive window %s", got, inactiveUsageWindow)
	}
}

// privateClaudeAccounts makes one active and one inactive Claude Code account,
// both signed in with a saved grant.
func privateClaudeAccounts(t *testing.T) (*runtime.Accounts, runtime.NativeAccount, runtime.NativeAccount, *store.Store) {
	t.Helper()
	accounts, bundle, active, st := privateClaudeAccount(t)
	inactive, err := accounts.Create("claude-code", "Inactive")
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeFixtureWithAccount(t, inactive.Home, "inactive-access", "aaaaaaaa-bbbb-cccc-dddd-000000000002", "inactive@example.test")
	if _, err = accounts.Capture(t.Context(), inactive.ID, bundle.Executable); err != nil {
		t.Fatal(err)
	}
	return accounts, active, inactive, st
}

func TestInactiveUsageProbesSavedGrantAtIntervalAndReset(t *testing.T) {
	accounts, active, inactive, st := privateClaudeAccounts(t)
	nativePath := filepath.Join(active.Home, ".credentials.json")
	before, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	reset := now.Add(time.Minute)
	used := 95.0
	if err = st.Update(func(state *store.State) error {
		state.Find(inactive.ID).Observation = &store.Observation{ObservedAt: now, LastAttemptAt: now, Windows: []Window{{ID: "seven_day", UsedPercent: &used, ResetsAt: reset.Format(time.RFC3339)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	old := http.DefaultTransport
	http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.anthropic.com" || r.Header.Get("Authorization") != "Bearer inactive-access" {
			t.Fatal("probe did not use the inactive saved grant")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":12,"resets_at":"2030-03-17T17:46:40Z"}}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	s := New(Options{Store: st, Now: func() time.Time { return now }})
	// Populate the snapshot cache to check that timer writes become visible.
	s.Snapshot(t.Context(), false)
	if next := s.RefreshInactiveUsage(t.Context(), now); !next.Equal(reset) || calls != 0 {
		t.Fatalf("before reset: next=%v calls=%d", next, calls)
	}
	now = reset
	if next := s.RefreshInactiveUsage(t.Context(), now); !next.Equal(now.Add(inactiveUsageWindow)) || calls != 1 {
		t.Fatalf("at reset: next=%v calls=%d", next, calls)
	}
	for _, row := range s.AccountUsageCached().Accounts {
		if row.ID == inactive.ID && (len(row.Windows) != 1 || row.Windows[0].UsedPercent == nil || *row.Windows[0].UsedPercent != 12) {
			t.Fatal("timer update did not reach the cached usage view")
		}
	}
	// A new service must honor the persisted attempt time after a restart.
	now = now.Add(inactiveUsageWindow - time.Minute)
	s = New(Options{Store: st, Now: func() time.Time { return now }})
	s.RefreshInactiveUsage(t.Context(), now)
	if calls != 1 {
		t.Fatal("inactive account probed before the inactive usage window")
	}
	now = now.Add(time.Minute)
	s.RefreshInactiveUsage(t.Context(), now)
	if calls != 2 {
		t.Fatalf("calls=%d, want interval probe", calls)
	}
	after, err := os.ReadFile(nativePath)
	if err != nil || string(before) != string(after) {
		t.Fatal("inactive probe changed active native credentials", err)
	}
	selected, err := accounts.Active(store.ProviderAnthropic, "claude-code")
	if err != nil || selected.ID != active.ID {
		t.Fatal("inactive probe changed the active account", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.RefreshInactiveUsage(ctx, now.Add(time.Hour))
	if calls != 2 {
		t.Fatal("cancelled pass probed an account")
	}
}

func TestActivePiAccountProbesItsProviderUsageWhileIdle(t *testing.T) {
	accounts := &runtime.Accounts{Root: t.TempDir()}
	account, err := accounts.CreateProvider("pi", store.ProviderDeepSeek, store.KindAPIKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(account.Home, "auth.json"), []byte(`{"deepseek":{"type":"api_key","key":"fixture-key"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Capture(t.Context(), account.ID, ""); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{Root: accounts.Root}
	if err := st.SetActive(store.ProviderDeepSeek, "pi", "", account.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	calls := 0
	old := http.DefaultTransport
	http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.deepseek.com/user/balance" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Fatal("wrong Pi usage request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"12.34"}]}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	s := New(Options{Store: st, Now: func() time.Time { return now }})
	if next := s.RefreshInactiveUsage(t.Context(), now); next.Before(now.Add(inactiveUsageWindow)) {
		t.Fatalf("next probe = %v", next)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want one idle probe", calls)
	}
	state, err := st.Read()
	if err != nil {
		t.Fatal(err)
	}
	entry := state.Find(account.ID)
	if entry == nil || entry.Observation == nil || len(entry.Observation.Windows) != 1 || entry.Observation.Windows[0].Balance == nil || *entry.Observation.Windows[0].Balance != 12.34 {
		t.Fatalf("observation = %+v", entry)
	}
}

func TestFailedInactiveResetProbeWaitsBeforeRetry(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	st := &store.Store{Root: t.TempDir()}
	id := "11111111111111111111111111111111"
	if err := st.Update(func(state *store.State) error {
		state.Entries = append(state.Entries, store.Entry{Metadata: store.Metadata{ID: id, Identity: "fixture", Observation: &store.Observation{
			ObservedAt: now.Add(-time.Minute), LastAttemptAt: now.Add(-time.Minute), Windows: []Window{{ResetsAt: now.Format(time.RFC3339)}},
		}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	probe := usageProbe(func(context.Context, native.Captured) (store.Observation, error) {
		calls++
		return store.Observation{}, errors.New("provider unavailable")
	})
	s := New(Options{Store: st, Now: func() time.Time { return now }})
	row := Account{ID: id, Harness: "claude-code"}
	captured := native.Captured{Identity: "fixture"}
	if err := s.refreshNativeUsage(t.Context(), &row, probe, captured); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("reset did not trigger a probe")
	}
	now = now.Add(time.Second)
	if err := s.refreshNativeUsage(t.Context(), &row, probe, captured); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("failed reset probe retried before its interval")
	}
}

func TestInactiveProbeDoesNotBlockProfileModels(t *testing.T) {
	_, _, _, st := privateClaudeAccounts(t)
	started := make(chan struct{})
	release := make(chan struct{})
	old := http.DefaultTransport
	http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return nil, errors.New("provider unavailable")
	})
	defer func() { http.DefaultTransport = old }()
	ctx, cancel := context.WithCancel(t.Context())
	s := New(Options{Store: st, Models: func(context.Context, string, string, []string) ([]adapters.Model, error) {
		return []adapters.Model{{ID: "fixture-model"}}, nil
	}})
	done := make(chan struct{})
	go func() { defer close(done); s.RefreshInactiveUsage(ctx, time.Now()) }()
	defer func() { close(release); cancel(); <-done }()
	<-started
	read := make(chan Catalog, 1)
	go func() {
		catalog, err := s.ProfileModels(ctx, "claude-code", false)
		if err != nil {
			catalog.Message = err.Error()
		}
		read <- catalog
	}()
	select {
	case snap := <-read:
		if len(snap.Models) != 1 || snap.Models[0].ID != "fixture-model" || snap.Message != "" {
			t.Fatal("model picker failed", snap.Message)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("model picker waited on an inactive provider request")
	}
}

// The active account's reading is re-polled every five minutes while turns
// run on its harness, and not while the harness is idle.
func TestRefreshInactiveUsageRepollsTheActiveAccountDuringTurns(t *testing.T) {
	accounts, active, inactive, st := privateClaudeAccounts(t)
	now := time.Now().UTC().Truncate(time.Second)
	used := 95.0
	if err := st.Update(func(state *store.State) error {
		// The inactive account's next probe is far away, so only the active
		// account can produce a call during the test.
		state.Find(inactive.ID).Observation = &store.Observation{ObservedAt: now, LastAttemptAt: now, Windows: []Window{{ID: "seven_day", UsedPercent: &used, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}}
		state.Find(active.ID).Observation = &store.Observation{ObservedAt: now.Add(-6 * time.Minute), LastAttemptAt: now.Add(-6 * time.Minute), Windows: []Window{{ID: "seven_day", UsedPercent: &used, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	old := http.DefaultTransport
	http.DefaultTransport = usageRefreshTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.anthropic.com" || r.Header.Get("Authorization") != "Bearer fixture-access" {
			t.Errorf("probe did not use the active live grant: %v %v", r.URL.Host, r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":12,"resets_at":"2030-03-17T17:46:40Z"}}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	s := New(Options{Store: st, Now: func() time.Time { return now }})
	// An idle harness keeps its reading: no probe before any turn runs.
	if next := s.RefreshInactiveUsage(t.Context(), now); !next.Equal(now.Add(inactiveUsageWindow)) || calls != 0 {
		t.Fatalf("idle: next=%v calls=%d, want no active probe", next, calls)
	}
	release, err := accounts.TurnLease(t.Context(), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// The reading became due six minutes ago; the probe stamps with the
	// manager's real clock, so the service clock must stay at `now` for the
	// fresh reading to read as fresh.
	s = New(Options{Store: st, Now: func() time.Time { return now }})
	if next := s.RefreshInactiveUsage(t.Context(), now); !next.Equal(now.Add(5*time.Minute)) || calls != 1 {
		t.Fatalf("running: next=%v calls=%d, want one active probe and the next in five minutes", next, calls)
	}
	for _, row := range s.AccountUsageCached().Accounts {
		if row.ID == active.ID {
			if len(row.Windows) != 1 || row.Windows[0].UsedPercent == nil || *row.Windows[0].UsedPercent != 12 {
				t.Fatalf("active reading = %#v, want the probed 12%%", row.Windows)
			}
			if row.Stale {
				t.Fatal("the fresh active reading is marked stale")
			}
		}
	}
}

func TestActiveUsageDue(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	if got := activeUsageDue(nil, now); !got.IsZero() {
		t.Fatal("an account without a reading is due at once")
	}
	if got := activeUsageDue(&store.Observation{ObservedAt: now, LastAttemptAt: now}, now); !got.Equal(now.Add(activeUsageRepoll)) {
		t.Fatalf("due = %v, want %v", got, now.Add(activeUsageRepoll))
	}
	backoff := now.Add(2 * time.Hour)
	if got := activeUsageDue(&store.Observation{ObservedAt: now, RateLimitedUntil: &backoff}, now); !got.Equal(backoff) {
		t.Fatalf("due = %v, want the rate limit to hold the next attempt", got)
	}
}
