package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// Usage lists only Dyna's private accounts. An installed harness with the
// owner's own login adds no row.
func TestSnapshotListsOnlyPrivateAccounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	s := New(Options{Store: store})
	s.installed = func() []Installation { return []Installation{{Harness: "codex", Executable: "/fixture/codex"}} }
	if got := s.Snapshot(context.Background(), false).Accounts; len(got) != 0 {
		t.Fatalf("Snapshot() listed personal rows: %+v", got)
	}
	n, err := (&runtime.Accounts{Root: store.Root}).Create("codex", "Work")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot(context.Background(), false).Accounts
	if len(got) != 1 || got[0].ID != n.ID || got[0].Label != "Work" || got[0].Source != "Dyna private native account" {
		t.Fatalf("Snapshot() = %+v, want the private account", got)
	}
}

func TestAccountUsageCachedShowsPrivateObservationsAndResets(t *testing.T) {
	now := time.Now().UTC()
	store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	reset := now.Add(time.Hour)
	used := 100.0
	privateID, grantID := "11111111111111111111111111111111", "22222222222222222222222222222222"
	if err := store.Update(func(state *accountstore.State) error {
		state.Entries = append(state.Entries,
			accountstore.Entry{Metadata: accountstore.Metadata{ID: privateID, Harness: "muse", Label: "Muse", NativeHome: filepath.Join(store.Root, "muse", privateID), Observation: &accountstore.Observation{
				Windows:    []Window{{ID: "subscription", UsedPercent: &used, Unit: "percent", ResetsAt: reset.Format(time.RFC3339Nano)}},
				ObservedAt: now, RateLimitedUntil: &reset,
			}}},
			accountstore.Entry{Metadata: accountstore.Metadata{ID: grantID, Harness: "codex", Identity: "codex:grant"}, Credential: accountstore.Credential{Schema: "codex/auth-file@1"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: store, Now: func() time.Time { return now }})
	rows := s.AccountUsageCached().Accounts
	if len(rows) != 1 || rows[0].ID != privateID {
		t.Fatalf("AccountUsageCached() = %+v, want only the private account", rows)
	}
	if len(rows[0].Windows) != 1 || *rows[0].Windows[0].UsedPercent != 100 || !rows[0].QuotaBlocked || !rows[0].QuotaBlockedUntil.Equal(reset) {
		t.Fatalf("exhaustion not displayed: %+v", rows[0])
	}
	now = reset
	if row := s.AccountUsageCached().Accounts[0]; row.Windows[0].UsedPercent != nil || row.Windows[0].UnavailableReason != "probably reset" || !row.Stale || row.QuotaBlocked {
		t.Fatalf("past quota must show uncertainty without blocking: %+v", row)
	}
}

func TestBlockingQuotaMetadata(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	pct, used, limit := 100.0, 10.0, 10.0
	soon, later := now.Add(time.Hour), now.Add(2*time.Hour)
	accounts := []Account{{Windows: []Window{
		{ID: "five_hour", UsedPercent: &pct, ResetsAt: soon.Format(time.RFC3339)},
		{ID: "seven_day", Used: &used, Limit: &limit, ResetsAt: later.Format(time.RFC3339)},
		{ID: "other", UsedPercent: &pct, ResetsAt: later.Add(time.Hour).Format(time.RFC3339)},
	}}}
	ApplyBlockingQuota(accounts, now)
	a := accounts[0]
	if !a.QuotaBlocked || a.QuotaBlockedUntil == nil || !a.QuotaBlockedUntil.Equal(later) || len(a.BlockingQuotaWindows) != 2 {
		t.Fatalf("blocking metadata: %+v", a)
	}
	ApplyBlockingQuota(accounts, later)
	if accounts[0].QuotaBlocked || accounts[0].QuotaBlockedUntil != nil || len(accounts[0].BlockingQuotaWindows) != 0 {
		t.Fatal("expired metadata retained")
	}
	for _, reset := range []string{"", "invalid", now.Format(time.RFC3339)} {
		accounts[0].Windows = []Window{{ID: "five_hour", UsedPercent: &pct, ResetsAt: reset}}
		ApplyBlockingQuota(accounts, now)
		if accounts[0].QuotaBlocked {
			t.Fatalf("invalid or expired reset blocks: %q", reset)
		}
	}
}

// A broken account index shows its error in Usage instead of an empty list.
func TestSnapshotReportsUnreadableAccountIndex(t *testing.T) {
	store := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root, "accounts.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	snap := New(Options{Store: store}).Snapshot(context.Background(), false)
	if len(snap.Accounts) != 0 || snap.Message == "" {
		t.Fatalf("Snapshot() = %+v; want the storage error", snap)
	}
}
