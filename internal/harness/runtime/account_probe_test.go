package runtime

import (
	"context"
	"errors"
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
	got, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "", "", now)
	if err != nil || !ok || got.ID != held.ID {
		t.Fatalf("StatedHoldAccount = %s, %v, %v; want %s", got.ID, ok, err, held.ID)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "", "", now.Add(4*time.Hour)); err != nil || !ok {
		t.Fatalf("after the first end, the later stated hold should remain: %v, %v", ok, err)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "", "", now.Add(6*time.Hour)); err != nil || ok {
		t.Fatalf("after every end, no account should be held: %v, %v", ok, err)
	}
}

// A stated hold for one model never selects an account for another model, because
// the provider's limit may cover only the model it named.
func TestStatedHoldAccountMatchesOnlyTheHeldModel(t *testing.T) {
	a := testAccounts(t)
	held := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.Hold(held.ID, reposcope.Hold{Until: now.Add(3 * time.Hour), Stated: true, Model: "model-a"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "model-b", "", now); err != nil || ok {
		t.Fatalf("a model-a hold selected an account for model-b: %v, %v", ok, err)
	}
	got, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "model-a", "", now)
	if err != nil || !ok || got.ID != held.ID {
		t.Fatalf("StatedHoldAccount for model-a = %s, %v, %v; want %s", got.ID, ok, err, held.ID)
	}
}

// A guarded switch refuses to replace an account that is no longer the one it
// expects, and it changes no login state.
func TestGuardedSwitchRefusesAConcurrentSwitch(t *testing.T) {
	a := testAccounts(t)
	expected := signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	moved := signedClaude(t, a, "other", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	target := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.Switch(context.Background(), moved.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	err := a.switchTo(context.Background(), target.ID, "/fake/native", nil, func(active string) bool {
		return active == expected.ID
	})
	if !errors.Is(err, errSwitchKept) {
		t.Fatalf("guarded switch error = %v; want errSwitchKept", err)
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != moved.ID {
		t.Fatalf("refused switch moved the active account to %s; want %s", active.ID, moved.ID)
	}
}

// A held account whose repo rules refuse the probe's repo is not probed there,
// because the probe would fail the account check before it could run.
func TestStatedHoldAccountSkipsAnAccountTheRepoRefuses(t *testing.T) {
	a, _, work := scopedClaudeAccounts(t)
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.MarkReset(work.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "", "", now); err != nil || ok {
		t.Fatalf("the work account may not run in no repo, so it must not be probed: %v, %v", ok, err)
	}
	got, ok, err := a.StatedHoldAccount("claude-code", store.ProviderAnthropic, "", workRepo, now)
	if err != nil || !ok || got.ID != work.ID {
		t.Fatalf("StatedHoldAccount in %s = %s, %v, %v; want %s", workRepo, got.ID, ok, err, work.ID)
	}
}

// The probe runs on the held account, then the account that was active before
// the probe becomes active again, so later turns keep their account.
func TestProbeRestoresThePreviousActiveAccount(t *testing.T) {
	a, personal, work := scopedClaudeAccounts(t)
	// The work account's repo rules would refuse a probe with no repo, so clear them.
	if err := a.SetRepos(work.ID, nil); err != nil {
		t.Fatal(err)
	}
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(work.ID, time.Now().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: a, Bundles: manager(t)}
	p := profile.Profile{Harness: "claude-code", RuntimeVersion: "1.2.3"}
	_, heldID, restore, err := r.SelectProbeAccount(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if heldID != work.ID {
		t.Fatalf("held account = %s, want %s", heldID, work.ID)
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != work.ID {
		t.Fatalf("probe ran on %s, want the held account %s", active.ID, work.ID)
	}
	restore()
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != personal.ID {
		t.Fatalf("after the probe, active = %s; want the previous account %s", active.ID, personal.ID)
	}
}

// A switch made by another turn during the probe stays active after the restore,
// so the restore never overwrites a choice it did not make.
func TestProbeRestoreKeepsAConcurrentSwitch(t *testing.T) {
	a, _, work := scopedClaudeAccounts(t)
	if err := a.SetRepos(work.ID, nil); err != nil {
		t.Fatal(err)
	}
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReset(work.ID, time.Now().Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{Accounts: a, Bundles: manager(t)}
	p := profile.Profile{Harness: "claude-code", RuntimeVersion: "1.2.3"}
	_, _, restore, err := r.SelectProbeAccount(context.Background(), p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := signedClaude(t, a, "other", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	if err := a.Switch(context.Background(), other.ID, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	restore()
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != other.ID {
		t.Fatalf("after the restore, active = %s; want the concurrent switch %s", active.ID, other.ID)
	}
}
