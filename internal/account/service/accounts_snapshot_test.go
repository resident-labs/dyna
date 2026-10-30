package service

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// The Accounts view groups by provider and marks both the global account and
// the account the requested room uses, so a room choice never hides the global
// one.
func TestAccountsMarksTheRoomAccountAndTheGlobalOne(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	accounts := &runtime.Accounts{Root: root}
	work, err := accounts.Create("claude-code", "Work")
	if err != nil {
		t.Fatal(err)
	}
	home, err := accounts.Create("claude-code", "Home")
	if err != nil {
		t.Fatal(err)
	}
	selections := &store.Store{Root: root}
	if err := selections.SetActive(store.ProviderAnthropic, "claude-code", "", work.ID); err != nil {
		t.Fatal(err)
	}
	if err := selections.SetActive(store.ProviderAnthropic, "claude-code", "room-a", home.ID); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: &store.Store{Root: root}})

	snap := s.Accounts(t.Context(), Scope{Room: "room-a"}, false)
	if len(snap.Providers) != 1 || snap.Providers[0].Provider != store.ProviderAnthropic || len(snap.Providers[0].Accounts) != 2 {
		t.Fatalf("Accounts = %+v, want one provider with both accounts", snap)
	}
	marks := map[string]ScopeAccount{}
	for _, row := range snap.Providers[0].Accounts {
		marks[row.Label] = row
	}
	if !marks["Home"].ActiveHere || marks["Home"].Active {
		t.Fatalf("room account = %+v, want the room mark only", marks["Home"])
	}
	if !marks["Work"].Active || marks["Work"].ActiveHere {
		t.Fatalf("global account = %+v, want the global mark only", marks["Work"])
	}

	// Without a room the global account is the one shown as active.
	global := s.Accounts(t.Context(), Scope{}, false)
	if !global.Providers[0].Accounts[0].ActiveHere || global.Providers[0].Accounts[0].Label != "Work" {
		t.Fatalf("global Accounts = %+v, want Work first and active", global)
	}

	// A room with no row of its own uses the global account, as the launcher
	// does. A room row that names no account overrides it with no selection.
	inherited := s.Accounts(t.Context(), Scope{Room: "room-b"}, false)
	if !inherited.Providers[0].Accounts[0].ActiveHere || inherited.Providers[0].Accounts[0].Label != "Work" {
		t.Fatalf("unset room = %+v, want the global account inherited", inherited)
	}
	if err := selections.SetActive(store.ProviderAnthropic, "claude-code", "room-b", ""); err != nil {
		t.Fatal(err)
	}
	cleared := s.Accounts(t.Context(), Scope{Room: "room-b"}, false)
	for _, row := range cleared.Providers[0].Accounts {
		if row.ActiveHere {
			t.Fatalf("room row that names no account still marks %s", row.Label)
		}
	}
}

// A room view marks the accounts whose repo limit lets them run in the room's
// repository, so the page can pool only the usage that room can draw on.
func TestAccountsMarksWhichAccountsServeTheRoomRepo(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "accounts")
	accounts := &runtime.Accounts{Root: root}
	open, err := accounts.Create("claude-code", "Open")
	if err != nil {
		t.Fatal(err)
	}
	limited, err := accounts.Create("claude-code", "Limited")
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := reposcope.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := scopes.Set(limited.ID, []string{"github.com/org/other"}); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: &store.Store{Root: root}})
	serves := func(sc Scope) map[string]bool {
		out := map[string]bool{}
		for _, row := range s.Accounts(t.Context(), sc, false).Providers[0].Accounts {
			out[row.ID] = row.ServesHere
		}
		return out
	}
	if got := serves(Scope{Room: "room-a", Repo: "github.com/org/app"}); !got[open.ID] || got[limited.ID] {
		t.Fatalf("room in another repo: serves = %v, want only the open account", got)
	}
	if got := serves(Scope{Room: "room-b", Repo: "github.com/org/other"}); got[open.ID] || !got[limited.ID] {
		t.Fatalf("room in the limited repo: serves = %v, want only the limited account", got)
	}
	if got := serves(Scope{}); !got[open.ID] || !got[limited.ID] {
		t.Fatalf("global view: serves = %v, want every account", got)
	}
}
