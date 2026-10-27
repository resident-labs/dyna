package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/profile"
)

const (
	workRepo     = "github.com/acmecorp/portal"
	personalRepo = "github.com/resident-labs/dyna"
)

// scopedClaudeAccounts signs in a personal and a work account, makes the
// personal one active, and limits the work account to workRepo.
func scopedClaudeAccounts(t *testing.T) (a *Accounts, personal, work NativeAccount) {
	t.Helper()
	a = testAccounts(t)
	personal = signedClaude(t, a, "personal", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	work = signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.transaction(func(*store.State) error { return a.setActive("claude-code", personal.ID) }, true); err != nil {
		t.Fatal(err)
	}
	s, err := reposcope.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Set(work.ID, []string{workRepo}); err != nil {
		t.Fatal(err)
	}
	return a, personal, work
}

func TestSelectForRepoSwitchesToTheAllowedAccount(t *testing.T) {
	a, personal, work := scopedClaudeAccounts(t)
	for _, c := range []struct{ repo, want string }{
		{workRepo, work.ID},
		{personalRepo, personal.ID},
		{"", personal.ID},
	} {
		if err := a.SelectForRepo(context.Background(), "claude-code", store.ProviderAnthropic, "", c.repo, "/fake/native"); err != nil {
			t.Fatalf("repo %q: %v", c.repo, err)
		}
		if active, err := a.Active(accountProvider("claude-code"), "claude-code"); err != nil || active.ID != c.want {
			t.Fatalf("repo %q: active = %v, %v; want %s", c.repo, active.ID, err, c.want)
		}
	}
}

func TestSelectForRepoNeverPicksARefusedAccount(t *testing.T) {
	a, personal, work := scopedClaudeAccounts(t)
	if err := a.transaction(func(s *store.State) error { s.Find(work.ID).NeedsLogin = true; return nil }, true); err != nil {
		t.Fatal(err)
	}
	err := a.SelectForRepo(context.Background(), "claude-code", store.ProviderAnthropic, "", workRepo, "/fake/native")
	if err == nil {
		t.Fatal("the personal account was allowed into a protected repo")
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != personal.ID {
		t.Fatalf("active changed to %s", active.ID)
	}
}

func TestActiveForRefusesADisallowedActiveAccount(t *testing.T) {
	a, _, _ := scopedClaudeAccounts(t)
	if _, err := a.activeFor("claude-code", store.ProviderAnthropic, "", workRepo); !errors.Is(err, errScopeRefused) {
		t.Fatalf("err = %v, want a scope refusal", err)
	}
	if _, err := a.activeFor("claude-code", store.ProviderAnthropic, "", personalRepo); err != nil {
		t.Fatal(err)
	}
}

func TestRemovingAnAccountFreesItsRepos(t *testing.T) {
	a, _, work := scopedClaudeAccounts(t)
	rules, err := a.rules()
	if err != nil || !rules.Protected(workRepo) {
		t.Fatal("setup: the work repo is not protected", err)
	}
	if err = a.Remove(work.ID, true); err != nil {
		t.Fatal(err)
	}
	if rules, err = a.rules(); err != nil || rules.Protected(workRepo) {
		t.Fatal("a removed account still protects its repo", err)
	}
}

func TestSelectAccountInstallsAMissingHarnessFirst(t *testing.T) {
	a, _, _ := scopedClaudeAccounts(t)
	r := &Runtime{Accounts: a, Bundles: manager(t)}
	p := profile.Profile{Harness: "claude-code", RuntimeVersion: "1.2.3"}
	if _, err := r.SelectAccount(context.Background(), p, t.TempDir()); err != nil {
		t.Fatal("a missing harness copy stopped the turn:", err)
	}
	if _, err := r.Bundles.Resolve("claude-code", "1.2.3"); err != nil {
		t.Fatal("the harness copy was not installed:", err)
	}
}

func TestFailoverMovesToTheAllowedAccountAndNeverToARefusedOne(t *testing.T) {
	a, personal, work := scopedClaudeAccounts(t)
	until := time.Now().Add(3 * time.Hour)
	// The work account runs a work-repo turn and hits its limit. The personal
	// account has no list, but the work repo is protected, so it is refused.
	if err := a.SelectForRepo(context.Background(), "claude-code", store.ProviderAnthropic, "", workRepo, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Failover(context.Background(), store.ProviderAnthropic, work.ID, workRepo, "/fake/native", until, true); err == nil || !strings.Contains(err.Error(), "resets") {
		t.Fatalf("err = %v, want the reset time", err)
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != work.ID {
		t.Fatalf("active moved to %s", active.ID)
	}
	// In another repo the personal account is allowed and takes over.
	if err := a.SelectForRepo(context.Background(), "claude-code", store.ProviderAnthropic, "", personalRepo, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	second := signedClaude(t, a, "second", "aaaaaaaa-bbbb-cccc-dddd-000000000003")
	from, to, err := a.Failover(context.Background(), store.ProviderAnthropic, personal.ID, personalRepo, "/fake/native", until, true)
	if err != nil || from.ID != personal.ID || to.ID != second.ID {
		t.Fatalf("from=%v to=%v err=%v", from.ID, to.ID, err)
	}
	if active, _ := a.Active(accountProvider("claude-code"), "claude-code"); active.ID != second.ID {
		t.Fatal("the account did not switch")
	}
}

// Turn A fails after another turn made B active. A is the one marked limited,
// and B, already active and allowed, takes over without a switch.
func TestFailoverBlamesTheFailedTurnNotTheActiveAccount(t *testing.T) {
	a := testAccounts(t)
	first := signedClaude(t, a, "first", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	second := signedClaude(t, a, "second", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.transaction(func(*store.State) error { return a.setActive("claude-code", second.ID) }, true); err != nil {
		t.Fatal(err)
	}
	from, to, err := a.Failover(context.Background(), store.ProviderAnthropic, first.ID, "", "/fake/native", time.Now().Add(time.Hour), true)
	if err != nil || from.ID != first.ID || to.ID != second.ID {
		t.Fatalf("from=%v to=%v err=%v", from.ID, to.ID, err)
	}
	s, _ := a.scopes()
	limits, _ := s.Limits(time.Now())
	if _, ok := limits[first.ID]; !ok || len(limits) != 1 {
		t.Fatalf("limits = %v, want only the failed turn's account", limits)
	}
}

// A repeat limit shorter than the stored one keeps the stored reset. With no
// account left, the error must name the stored reset, not the shorter one.
func TestFailoverKeepsTheStoredResetForARepeatLimit(t *testing.T) {
	a := testAccounts(t)
	first := signedClaude(t, a, "first", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	second := signedClaude(t, a, "second", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	if err := a.transaction(func(*store.State) error { return a.setActive("claude-code", first.ID) }, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	twoHours := now.Add(2 * time.Hour)
	if _, _, err := a.Failover(context.Background(), store.ProviderAnthropic, first.ID, "", "/fake/native", twoHours, true); err != nil {
		t.Fatal(err)
	}
	// The second account is now limited too, so no account is left to take over.
	_, _, _ = a.Failover(context.Background(), store.ProviderAnthropic, second.ID, "", "/fake/native", now.Add(3*time.Hour), true)
	_, _, err := a.Failover(context.Background(), store.ProviderAnthropic, first.ID, "", "/fake/native", now.Add(time.Minute), true)
	want := "resets " + twoHours.Local().Format("Mon 15:04")
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

func TestFailoverNamesNoResetTimeWhenTheProviderStatedNone(t *testing.T) {
	a, _, work := scopedClaudeAccounts(t)
	if err := a.SelectForRepo(context.Background(), "claude-code", store.ProviderAnthropic, "", workRepo, "/fake/native"); err != nil {
		t.Fatal(err)
	}
	guess := time.Now().Add(time.Hour)
	_, _, err := a.Failover(context.Background(), store.ProviderAnthropic, work.ID, workRepo, "/fake/native", guess, false)
	if err == nil || strings.Contains(err.Error(), guess.Local().Format("Mon 15:04")) || !strings.Contains(err.Error(), "no reset time") {
		t.Fatalf("err = %v, want no guessed reset time", err)
	}
}
