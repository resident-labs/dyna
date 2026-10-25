package service

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// The Accounts view groups by provider and marks both the global account and
// the account the requested room uses, so a room choice never hides the global
// one.
func TestAccountsMarksTheRoomAccountAndTheGlobalOne(t *testing.T) {
	root := filepath.Join(t.TempDir(), "accounts")
	accounts := &runtime.Accounts{Root: root}
	work, err := accounts.Create("codex", "Work")
	if err != nil {
		t.Fatal(err)
	}
	home, err := accounts.Create("codex", "Home")
	if err != nil {
		t.Fatal(err)
	}
	selections := &store.Store{Root: root}
	if err := selections.SetActive(store.ProviderChatGPT, "codex", "", work.ID); err != nil {
		t.Fatal(err)
	}
	if err := selections.SetActive(store.ProviderChatGPT, "codex", "room-a", home.ID); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: &store.Store{Root: root}})

	snap := s.Accounts(t.Context(), "room-a", false)
	if len(snap.Providers) != 1 || snap.Providers[0].Provider != store.ProviderChatGPT || len(snap.Providers[0].Accounts) != 2 {
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
	global := s.Accounts(t.Context(), "", false)
	if !global.Providers[0].Accounts[0].ActiveHere || global.Providers[0].Accounts[0].Label != "Work" {
		t.Fatalf("global Accounts = %+v, want Work first and active", global)
	}

	// A room with no row of its own uses the global account, as the launcher
	// does. A room row that names no account overrides it with no selection.
	inherited := s.Accounts(t.Context(), "room-b", false)
	if !inherited.Providers[0].Accounts[0].ActiveHere || inherited.Providers[0].Accounts[0].Label != "Work" {
		t.Fatalf("unset room = %+v, want the global account inherited", inherited)
	}
	if err := selections.SetActive(store.ProviderChatGPT, "codex", "room-b", ""); err != nil {
		t.Fatal(err)
	}
	cleared := s.Accounts(t.Context(), "room-b", false)
	for _, row := range cleared.Providers[0].Accounts {
		if row.ActiveHere {
			t.Fatalf("room row that names no account still marks %s", row.Label)
		}
	}
}
