package accounts

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/importer"
	"dyna-agent/internal/account/service"
	"dyna-agent/internal/account/store"
)

// fakeEngine answers the account calls a list and a use make. A use writes a
// real selection row, so a test can see which scope changed.
type fakeEngine struct {
	root      string
	accounts  []service.ScopeAccount
	used      [][2]string
	imported  []any
	refreshed []bool
	result    importer.Result
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
		refresh, _ := args[1].(bool)
		f.refreshed = append(f.refreshed, refresh)
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
	case "AccountsImport":
		f.imported = args
		if target, ok := out.(*importer.Result); ok {
			*target = f.result
		}
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

// A dry-run import renders the typed refusal and names the profile that still
// runs the harness, and a move names the account it went to.
func TestImportRendersTheTypedAnswer(t *testing.T) {
	fake := newFake(t)
	fake.result = importer.Result{Harness: "codex", DryRun: true, Moved: []importer.Moved{}, Refused: []importer.Refused{{
		Reason: importer.ReasonInUse, Profiles: []string{"sol"},
	}}}
	cmd := Command(Options{Client: fake})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--import", "codex", "--dry-run"})
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("a refused import reported success")
	}
	if len(fake.imported) != 3 || fake.imported[0] != "codex" || fake.imported[2] != true {
		t.Fatalf("engine call = %v, want harness, account, dry run", fake.imported)
	}
	if text := out.String(); !strings.Contains(text, "sol") || !strings.Contains(text, "enabled profile") {
		t.Fatalf("import output = %q, want the profile named", text)
	}

	fake.result = importer.Result{Harness: "codex", Moved: []importer.Moved{{Source: "source", Label: "Work", Account: "pi-account", Provider: store.ProviderChatGPT, Kind: store.KindOAuth}}}
	cmd = Command(Options{Client: fake})
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"add", "--import", "codex"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("a move failed: %v", err)
	}
	if text := out.String(); !strings.Contains(text, "pi-account") || !strings.Contains(text, "Work") {
		t.Fatalf("import output = %q, want the moved account", text)
	}
}

// A saved reading shows its age and a stale mark, so an old percentage never
// looks current.
func TestListShowsTheReadingAgeAndStaleMark(t *testing.T) {
	used := 41.0
	snap := service.AccountsSnapshot{Providers: []service.ProviderAccounts{{Provider: store.ProviderAnthropic, Accounts: []service.ScopeAccount{{
		Account:    service.Account{ID: strings.Repeat("a", 32), Provider: store.ProviderAnthropic, Label: "Work", Status: "ready", ObservedAt: time.Now().Add(-3 * time.Minute), Stale: true, Windows: []service.Window{{ID: "weekly", Label: "weekly", UsedPercent: &used}}},
		ActiveHere: true,
	}}}}}
	var out bytes.Buffer
	writeAccounts(&out, snap, "")
	text := out.String()
	if !strings.Contains(text, "weekly 41%") || !strings.Contains(text, "observed 3m") || !strings.Contains(text, "(stale)") {
		t.Fatalf("list = %q, want the usage, its age and the stale mark", text)
	}
}

func TestListShowsLastRenewal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     time.Time
		result store.RenewalResult
		reason string
		want   string
	}{
		{name: "renewed", at: time.Now().Add(-4 * time.Minute), result: store.RenewalResultRenewed, want: "renewed 4m ago"},
		{name: "invalid grant", at: time.Now().Add(-2 * time.Minute), result: store.RenewalResultInvalidGrant, reason: "invalid_grant", want: "renewal failed 2m ago: invalid_grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := service.AccountsSnapshot{Providers: []service.ProviderAccounts{{Provider: store.ProviderAnthropic, Accounts: []service.ScopeAccount{{
				Account: service.Account{ID: strings.Repeat("a", 32), Provider: store.ProviderAnthropic, Label: "Work", Status: "ready", LastRenewal: &store.Renewal{
					AttemptedAt: tc.at, Result: tc.result, Reason: tc.reason,
				}},
			}}}}}
			var out bytes.Buffer
			writeAccounts(&out, snap, "")
			if text := out.String(); !strings.Contains(text, tc.want) {
				t.Fatalf("list = %q, want %q", text, tc.want)
			}
		})
	}
}

// D-148: a forced refresh is one short command, because rule r5 still names the
// removed `dyna usage --refresh` and `accounts list --refresh` is easy to miss.
func TestRefreshAsksEveryProviderForCurrentUsage(t *testing.T) {
	run := func(args ...string) (*fakeEngine, string) {
		t.Helper()
		fake := newFake(t)
		cmd := Command(Options{Client: fake})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err, out.String())
		}
		return fake, out.String()
	}
	fake, text := run("refresh")
	if len(fake.refreshed) != 1 || !fake.refreshed[0] {
		t.Fatalf("refresh asked for %v, want one forced refresh", fake.refreshed)
	}
	if !strings.Contains(text, "Work") || !strings.Contains(text, "Home") {
		t.Fatalf("refresh did not list the accounts: %s", text)
	}
	// A plain list keeps the saved readings.
	fake, _ = run("list")
	if len(fake.refreshed) != 1 || fake.refreshed[0] {
		t.Fatalf("list asked for %v, want the saved readings", fake.refreshed)
	}
}
