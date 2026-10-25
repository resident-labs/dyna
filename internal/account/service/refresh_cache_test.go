package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
)

type usageProbe func(context.Context, native.Captured) (accountstore.Observation, error)

func (probe usageProbe) ProbeSaved(ctx context.Context, captured native.Captured) (accountstore.Observation, error) {
	return probe(ctx, captured)
}

func TestRefreshPersistsObservationForPlainUsageRead(t *testing.T) {
	now := time.Now().UTC()
	oldUsed, newUsed := 21.0, 27.0
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "provider_failure"}[failure], func(t *testing.T) {
			store := &accountstore.Store{Root: t.TempDir()}
			id := "11111111111111111111111111111111"
			old := accountstore.Observation{ObservedAt: now.Add(-time.Hour), Windows: []Window{{ID: "session", UsedPercent: &oldUsed}}, Unavailable: true, Failures: 3}
			credential := accountstore.Credential{Schema: "fixture", Payload: []byte(`{"secret":"keep"}`)}
			if err := store.Update(func(state *accountstore.State) error {
				state.Entries = append(state.Entries, accountstore.Entry{Metadata: accountstore.Metadata{ID: id, Harness: "claude-code", Label: "Work", Identity: "fixture", NativeHome: filepath.Join(store.Root, "claude-code", id), Observation: &old}, Credential: credential})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			fresh := accountstore.Observation{ObservedAt: now, Plan: &Plan{ID: "max"}, Windows: []Window{{ID: "session", UsedPercent: &newUsed}}}
			calls := 0
			prober := usageProbe(func(_ context.Context, captured native.Captured) (accountstore.Observation, error) {
				calls++
				if captured.Identity != "fixture" {
					t.Fatal("wrong captured identity")
				}
				if failure {
					return accountstore.Observation{}, errors.New("provider unavailable")
				}
				return fresh, nil
			})
			service := New(Options{Store: store, Now: func() time.Time { return now }})
			row := Account{ID: id, Harness: "claude-code", Status: "ready"}
			if err := service.refreshNativeUsage(t.Context(), &row, prober, native.Captured{Identity: "fixture"}); err != nil {
				t.Fatal(err)
			}
			// This reader is also used by plain dyna accounts list; it has no service cache.
			plain := CachedUsage(store, now)
			if plain.Message != "" || len(plain.Accounts) != 1 || calls != 1 {
				t.Fatalf("plain usage = %+v, calls=%d", plain, calls)
			}
			got := plain.Accounts[0]
			if failure {
				for _, account := range []Account{row, got} {
					raw, _ := json.Marshal(account)
					var view map[string]any
					_ = json.Unmarshal(raw, &view)
					if view["usageError"] != "provider unavailable" {
						t.Fatalf("failed probe error missing from account: %s", raw)
					}
					if !strings.Contains(WindowsSummary(account), "provider unavailable") {
						t.Fatalf("usage summary hid probe error: %s", WindowsSummary(account))
					}
				}
				if *got.Windows[0].UsedPercent != oldUsed || !got.ObservedAt.Equal(old.ObservedAt) || !got.Stale {
					t.Fatalf("failed probe lost saved usage: %+v", got)
				}
			} else if *got.Windows[0].UsedPercent != newUsed || !got.ObservedAt.Equal(now) || got.Stale || got.Plan == nil || got.Plan.ID != "max" {
				t.Fatalf("plain usage did not get fresh observation: %+v", got)
			}
			state, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			entry := state.Find(id)
			var saved struct {
				Secret string `json:"secret"`
			}
			if err := json.Unmarshal(entry.Credential.Payload, &saved); err != nil || saved.Secret != "keep" || entry.Credential.Schema != credential.Schema {
				t.Fatal("observation update changed credentials")
			}
			if !failure && (entry.Observation.Unavailable || entry.Observation.Failures != 0) {
				t.Fatal("success retained failed observation metadata")
			}
		})
	}
}

func TestRefreshReportsSaveFailureWithoutRecreatingAccount(t *testing.T) {
	store := &accountstore.Store{Root: t.TempDir()}
	service := New(Options{Store: store})
	row := Account{ID: "11111111111111111111111111111111"}
	prober := usageProbe(func(context.Context, native.Captured) (accountstore.Observation, error) {
		return accountstore.Observation{ObservedAt: time.Now()}, nil
	})
	if err := service.refreshNativeUsage(t.Context(), &row, prober, native.Captured{Identity: "deleted"}); err == nil {
		t.Fatal("missing account must report a save error")
	}
	state, err := store.Read()
	if err != nil || len(state.Entries) != 0 {
		t.Fatalf("refresh recreated missing account: %+v, %v", state, err)
	}
}

func TestUsageRefreshSharesIntervalAndProviderBackoff(t *testing.T) {
	for _, retry := range []time.Duration{0, 30 * time.Second, 5 * time.Minute} {
		t.Run(retry.String(), func(t *testing.T) {
			now := time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)
			oldTime := now.Add(-time.Hour)
			used := 24.0
			store := &accountstore.Store{Root: t.TempDir()}
			id := "11111111111111111111111111111111"
			if err := store.Update(func(state *accountstore.State) error {
				state.Entries = append(state.Entries, accountstore.Entry{Metadata: accountstore.Metadata{
					ID: id, Harness: "claude-code", Identity: "fixture", NativeHome: filepath.Join(store.Root, "native"),
					Observation: &accountstore.Observation{ObservedAt: oldTime, Windows: []Window{{ID: "five_hour", UsedPercent: &used}}},
				}})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			probe := usageProbe(func(context.Context, native.Captured) (accountstore.Observation, error) {
				calls++
				if retry > 0 && calls == 1 {
					return accountstore.Observation{}, &native.QuotaError{Status: 429, RetryAfter: retry}
				}
				return accountstore.Observation{ObservedAt: now, Windows: []Window{{ID: "five_hour", UsedPercent: &used}}}, nil
			})
			refresh := func() Account {
				// A new service also reads the saved interval, as after an engine restart.
				s := New(Options{Store: store, Now: func() time.Time { return now }})
				row := Account{ID: id, Harness: "claude-code", Active: true}
				state, err := store.Read()
				if err != nil {
					t.Fatal(err)
				}
				applySavedObservation(&row, *state.Find(id), now)
				if err := s.refreshNativeUsage(t.Context(), &row, probe, native.Captured{Identity: "fixture"}); err != nil {
					t.Fatal(err)
				}
				return row
			}
			row := refresh()
			if retry > 0 {
				if row.RateLimitedUntil == nil || !row.RateLimitedUntil.Equal(now.Add(max(retry, 2*time.Minute))) {
					t.Fatalf("backoff missing: %+v", row)
				}
				want := "rate limited; showing the reading from " + oldTime.Format(time.RFC3339)
				for _, a := range []Account{row, CachedUsage(store, now).Accounts[0]} {
					if summary := WindowsSummary(a); !strings.Contains(summary, want) || strings.Contains(summary, "Usage unavailable") {
						t.Fatalf("summary = %s", summary)
					}
				}
			}
			wait := 2 * time.Minute
			if retry > wait {
				wait = retry
			}
			now = now.Add(wait - time.Nanosecond)
			refresh()
			if calls != 1 {
				t.Fatalf("provider called during saved interval: %d", calls)
			}
			now = now.Add(time.Nanosecond)
			row = refresh()
			if calls != 2 {
				t.Fatalf("provider not called at boundary: %d", calls)
			}
			if row.RateLimitedUntil != nil || row.UsageError != "" || row.Stale {
				t.Fatalf("success kept failure state: %+v", row)
			}
		})
	}
}
