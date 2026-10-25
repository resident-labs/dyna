package accounts

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/account/service"
	"dyna-agent/internal/account/store"
)

// fakeEngine answers the account calls a list and a use make. A use writes a
// real selection row, so a test can see which scope changed.
type fakeEngine struct {
	root     string
	accounts []service.ScopeAccount
	used     [][2]string
}

func newFake(t *testing.T) *fakeEngine {
	t.Helper()
	return &fakeEngine{root: filepath.Join(t.TempDir(), "accounts"), accounts: []service.ScopeAccount{
		{Account: service.Account{ID: strings.Repeat("a", 32), Provider: store.ProviderAnthropic, Kind: store.KindOAuth, Label: "Work", Status: "ready"}},
		{Account: service.Account{ID: strings.Repeat("b", 32), Provider: store.ProviderAnthropic, Kind: store.KindOAuth, Label: "Home", Status: "ready"}},
	}}
}

func (f *fakeEngine) Call(_ context.Context, method string, args []any, out any) error {
	switch method {
	case "AccountsList":
		snap, ok := out.(*service.AccountsSnapshot)
		if !ok {
			return nil
		}
		scope, _ := args[0].(string)
		active, _ := (&store.Store{Root: f.root}).ActiveFor(store.ProviderAnthropic, "claude-code", scope)
		rows := make([]service.ScopeAccount, len(f.accounts))
		for i, a := range f.accounts {
			a.ActiveHere = a.ID == active
			rows[i] = a
		}
		*snap = service.AccountsSnapshot{Scope: scope, Providers: []service.ProviderAccounts{{Provider: store.ProviderAnthropic, Accounts: rows}}}
	case "AccountsUse":
		id, _ := args[0].(string)
		scope, _ := args[1].(string)
		f.used = append(f.used, [2]string{id, scope})
		return (&store.Store{Root: f.root}).SetActive(store.ProviderAnthropic, "claude-code", scope, id)
	}
	return nil
}

// A room selection changes only that room, and --global leaves every room
// selection in place.
func TestUseRoomChangesOnlyThatRoom(t *testing.T) {
	fake := newFake(t)
	global, room := fake.accounts[0].ID, fake.accounts[1].ID
	if err := (&store.Store{Root: fake.root}).SetActive(store.ProviderAnthropic, "claude-code", "", global); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := Command(Options{Client: fake})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err, out.String())
		}
	}
	active := func(scope string) string {
		t.Helper()
		id, err := (&store.Store{Root: fake.root}).ActiveFor(store.ProviderAnthropic, "claude-code", scope)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	run("use", "Home", "--room", "room-a")
	if got := active("room-a"); got != room {
		t.Fatalf("room-a = %s, want the room account", got)
	}
	if got := active(""); got != global {
		t.Fatalf("global = %s, want the global account kept", got)
	}

	run("use", fake.accounts[0].ID[:6], "--global")
	if got := active(""); got != global {
		t.Fatalf("global = %s, want the ID prefix account", got)
	}
	if got := active("room-a"); got != room {
		t.Fatalf("room-a = %s, want the room account kept", got)
	}
	if len(fake.used) != 2 || fake.used[0][1] != "room-a" || fake.used[1][1] != "" {
		t.Fatalf("engine calls = %v, want a room use then a global use", fake.used)
	}
}
