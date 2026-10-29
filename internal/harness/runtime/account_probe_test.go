package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/profile"
)

// A probe must run on the account whose stated limit ends first. An unstated
// hold and a later stated one do not win.
func TestStatedHoldAccountIsTheStatedHoldThatEndsFirst(t *testing.T) {
	a := testAccounts(t)
	unstated := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	held := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	later := signedClaude(t, a, "other", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.MarkLimited(unstated.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(held.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(later.ID, now.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, now)
	if err != nil || !ok || got.ID != held.ID {
		t.Fatalf("StatedHoldAccount = %s, %v, %v; want %s", got.ID, ok, err, held.ID)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, now.Add(4*time.Hour)); err != nil || !ok {
		t.Fatalf("after the first end, the later stated hold should remain: %v, %v", ok, err)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, now.Add(6*time.Hour)); err != nil || ok {
		t.Fatalf("after every end, no account should be held: %v, %v", ok, err)
	}
}

// The probe runs on the held account, then the account that was active before
// the probe becomes active again, so later turns keep their account.
func TestProbeRestoresThePreviousActiveAccount(t *testing.T) {
	a, personal, work := scopedClaudeAccounts(t)
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(work.ID, time.Now().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: a, Bundles: manager(t)}
	p := profile.Profile{Harness: "claude-code", RuntimeVersion: "1.2.3"}
	_, restore, err := r.SelectProbeAccount(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != work.ID {
		t.Fatalf("probe ran on %s, want the held account %s", active.ID, work.ID)
	}
	restore()
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != personal.ID {
		t.Fatalf("after the probe, active = %s; want the previous account %s", active.ID, personal.ID)
	}
}
