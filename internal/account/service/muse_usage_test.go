package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/native"
	accountstore "dyna-agent/internal/account/store"
)

func TestMuseIdleRefreshRetainsSavedQuotaAndTimestamp(t *testing.T) {
	now := time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC)
	used := 21.0
	for _, hasPrevious := range []bool{true, false} {
		store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
		id := "11111111111111111111111111111111"
		identity := "meta:email:fixture@example.test"
		observed := now.Add(-6 * time.Hour)
		reset := now.Add(-time.Hour).Format(time.RFC3339)
		weeklyReset := now.Add(6 * 24 * time.Hour).Format(time.RFC3339)
		err := store.Update(func(s *accountstore.State) error {
			e := accountstore.Entry{Metadata: accountstore.Metadata{ID: id, Harness: "muse", Identity: identity, NativeHome: filepath.Join(store.Root, "muse", id)}}
			if hasPrevious {
				e.Observation = &accountstore.Observation{ObservedAt: observed, UsageError: "old unsupported message", Unavailable: true, Windows: []Window{{ID: "subscription", Label: "Session (5-hour)", UsedPercent: &used, ResetsAt: reset}, {ID: "weekly", Label: "Weekly", UsedPercent: &used, ResetsAt: weeklyReset}}}
			}
			s.Entries = append(s.Entries, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		service := New(Options{Store: store, Now: func() time.Time { return now }})
		row := Account{ID: id, Harness: "muse", Status: "ready"}
		prober := usageProbe(func(_ context.Context, _ native.Captured) (accountstore.Observation, error) {
			return accountstore.Observation{Plan: &Plan{ID: "max", Label: "Muse Code Max Usage"}}, native.ErrUsageIdle
		})
		if err := service.refreshNativeUsage(t.Context(), &row, prober, native.Captured{Identity: identity}); err != nil {
			t.Fatal(err)
		}
		got := CachedUsage(store, now).Accounts[0]
		if got.UsageError != "" || got.Status == "unavailable" || !strings.Contains(WindowsSummary(got), "idle") || got.Plan == nil || got.Plan.Label != "Muse Code Max Usage" {
			t.Fatal("idle quota appeared as an error", got)
		}
		if hasPrevious {
			if got.Stale {
				t.Fatal("successful idle check marked stale", got)
			}
			if len(got.Windows) != 2 || got.Windows[0].UsedPercent != nil || got.Windows[0].UnavailableReason != "probably reset" || got.Windows[1].UsedPercent == nil || *got.Windows[1].UsedPercent != used || got.Windows[0].ResetsAt != reset || got.Windows[1].ResetsAt != weeklyReset || !got.ObservedAt.Equal(observed) {
				t.Fatal("idle probe lost saved reset times or displayed unverified usage", got)
			}
			state, err := store.Read()
			if err != nil || *state.Find(id).Observation.Windows[0].UsedPercent != used {
				t.Fatal("reset projection changed the saved reading", err)
			}
		} else if !got.ObservedAt.IsZero() || got.Windows[0].UsedPercent != nil {
			t.Fatal("idle probe invented usage", got)
		}
	}
}

type museUsageTransport func(*http.Request) (*http.Response, error)

func (f museUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMuseUsageTokenFailureKeepsAPIKeyLoginAndSavedQuota(t *testing.T) {
	for _, expired := range []bool{true, false} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			now := time.Now().UTC()
			expiry := now.Add(time.Hour)
			if expired {
				expiry = now.Add(-time.Hour)
			}
			meta, _ := json.Marshal(map[string]any{"access_token": "fixture-access", "api_key": "fixture-api-key", "user_email": "fixture@example.test", "expires_at": expiry.Unix()})
			payload, _ := json.Marshal(map[string]json.RawMessage{"metaCredential": meta})
			captured := native.Captured{Identity: "meta:email:fixture@example.test", Credential: accountstore.Credential{Schema: "muse/auth-file@1", Payload: payload}}
			store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
			used := 21.0
			observed := now.Add(-time.Hour)
			var id string
			if err := store.Update(func(state *accountstore.State) error {
				entry, err := state.Save("muse", "", captured.Identity, "Muse", captured.Credential)
				if err != nil {
					return err
				}
				id = entry.ID
				state.Find(id).Observation = &accountstore.Observation{ObservedAt: observed, Plan: &Plan{ID: "max"}, Windows: []Window{{ID: "subscription", UsedPercent: &used, ResetsAt: now.Add(time.Hour).Format(time.RFC3339)}}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			calls := 0
			http.DefaultTransport = museUsageTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private-provider-error"))}, nil
			})
			manager, _ := native.For("muse", native.Host{})
			service := New(Options{Store: store, Now: func() time.Time { return now }})
			row := Account{ID: id, Harness: "muse", Status: "ready", ObservedAt: observed, Plan: &Plan{ID: "max"}, Windows: []Window{{ID: "subscription", UsedPercent: &used}}}
			if err := service.refreshNativeUsage(t.Context(), &row, manager.(native.SavedProber), captured); err != nil {
				t.Fatal(err)
			}
			if row.NeedsLogin || row.Status == "needs_auth" || row.UsageError == "" || strings.Contains(row.UsageError, "private-provider-error") {
				t.Fatal("usage-only failure invalidated working API-key login", row)
			}
			state, err := store.Read()
			if err != nil {
				t.Fatal(err)
			}
			saved := state.Find(id)
			if saved.NeedsLogin || saved.Observation.Plan == nil || len(saved.Observation.Windows) != 1 || *saved.Observation.Windows[0].UsedPercent != used || !saved.Observation.ObservedAt.Equal(observed) {
				t.Fatal("usage-only failure discarded saved quota", saved.Observation)
			}
			expected := 1
			if expired {
				expected = 0
			}
			if calls != expected {
				t.Fatalf("requests=%d want %d", calls, expected)
			}
		})
	}
}
