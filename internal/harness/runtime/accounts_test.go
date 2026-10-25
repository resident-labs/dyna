package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
	harnesshome "dyna-agent/internal/harness/home"
)

func testAccounts(t *testing.T) *Accounts {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Login preparation resolves its Dyna bridge from PATH. Keep tests
	// independent of a Dyna installation on the developer or CI host.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "dyna"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	clearRuntimeSelectors(t)
	for _, key := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(home, key))
	}
	return NewAccounts()
}

// clearRuntimeSelectors empties every harness home selector, so a test run
// inside a Dyna session does not read that session's private homes.
func clearRuntimeSelectors(t *testing.T) {
	t.Helper()
	for key := range harnesshome.Discovery("/unused") {
		t.Setenv(key, "")
	}
}
func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != want {
		t.Fatalf("mode for %s: %v %v", path, st, err)
	}
}
func writeAccountFixture(t *testing.T, n NativeAccount) {
	t.Helper()
	write := func(name, raw string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(n.Home, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n.Harness == "claude-code" {
		write(".credentials.json", `{"claudeAiOauth":{"accessToken":"native-access-canary","refreshToken":"native-refresh-canary","expiresAt":1999999999999,"scopes":["user:inference"]}}`)
		write(".claude.json", `{"oauthAccount":{"accountUuid":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","emailAddress":"fixture@example.test"}}`)
	} else {
		jwt := "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"fixture-user","chatgpt_account_id":"fixture-workspace"}}`)) + ".signature"
		raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": jwt, "access_token": "native-access-canary", "refresh_token": "native-refresh-canary", "account_id": "fixture-workspace"}})
		write("auth.json", string(raw))
	}
}
func TestAccountsPrivateLifecycle(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			a := testAccounts(t)
			t.Setenv("OPENAI_API_KEY", "inherited-key")
			t.Setenv("ANTHROPIC_API_KEY", "inherited-key")
			t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "/untrusted")
			beforeHome := os.Getenv("HOME")
			beforeXDG := os.Getenv("XDG_CONFIG_HOME")
			n, err := a.Create(h, "custom")
			if err != nil {
				t.Fatal(err)
			}
			checkMode(t, a.Root, 0700)
			checkMode(t, filepath.Dir(n.Home), 0700)
			checkMode(t, n.Home, 0700)
			if n.Status != "not signed in" || n.Harness == "claude" {
				t.Fatal(n)
			}
			n, err = a.Capture(context.Background(), n.ID, "/fake/native")
			if err != nil || n.Status != "not signed in" {
				t.Fatal(n, err)
			}
			argv, env, cwd, err := a.Login(n.ID, "/fake/native")
			if err != nil || argv[0] != "/fake/native" || cwd != n.Home {
				t.Fatal(argv, cwd, err)
			}
			for _, v := range env {
				if strings.HasPrefix(v, "HOME=") && v != "HOME="+n.Home+".home" {
					t.Fatal(v)
				}
			}
			if _, err := a.Env(n.ID); err == nil {
				t.Fatal("inactive account received launch environment")
			}
			writeAccountFixture(t, n)
			// Simulate a native login that used a permissive file mode.
			files, _ := os.ReadDir(n.Home)
			for _, f := range files {
				if err := os.Chmod(filepath.Join(n.Home, f.Name()), 0644); err != nil {
					t.Fatal(err)
				}
			}
			n, err = a.Capture(context.Background(), n.ID, "/fake/native")
			if err != nil || n.Status != "ready" || n.Identity == "" || n.Label != "custom" {
				t.Fatal(n, err)
			}
			for _, f := range files {
				checkMode(t, filepath.Join(n.Home, f.Name()), 0600)
			}
			raw := accountMetadataBytes(t, a)
			if strings.Contains(string(raw), "native-access-canary") || strings.Contains(string(raw), "native-refresh-canary") {
				t.Fatal("native grant entered metadata")
			}
			checkMode(t, filepath.Join(filepath.Dir(a.Root), "dyna.db"), 0600)
			if os.Getenv("HOME") != beforeHome || os.Getenv("XDG_CONFIG_HOME") != beforeXDG {
				t.Fatal("environment changed")
			}
			if err = a.Switch(context.Background(), n.ID, "/fake/native"); err != nil {
				t.Fatal(err)
			}
			shared, err := a.HarnessHome(n.Harness)
			if err != nil {
				t.Fatal(err)
			}
			overrides, err := a.Env(n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if h == "codex" && (overrides["CODEX_HOME"] != shared || overrides["CODEX_SQLITE_HOME"] != filepath.Join(shared, "sqlite") || overrides["OPENAI_API_KEY"] != "") {
				t.Fatal(overrides)
			}
			// Claude credentials stay in the account's own home; see claude_pin.go.
			if h == "claude" && (overrides["CLAUDE_CONFIG_DIR"] != shared || overrides["CLAUDE_SECURESTORAGE_CONFIG_DIR"] != n.Home || overrides["ANTHROPIC_API_KEY"] != "") {
				t.Fatal(overrides)
			}
			var wg sync.WaitGroup
			for range 12 {
				wg.Go(func() {
					m, e := a.Manager(n.ID, "/fake/native")
					if e != nil {
						t.Error(e)
						return
					}
					got, e := m.Capture(context.Background(), "")
					if e != nil || len(got) != 1 || got[0].Identity != n.Identity {
						t.Error("concurrent capture", e)
					}
					if _, e = m.Activate(context.Background(), "", store.Credential{}); e != native.ErrUnsupported {
						t.Error("activation allowed", e)
					}
				})
			}
			wg.Wait()
			other, err := a.Create(h, "other")
			if err != nil {
				t.Fatal(err)
			}
			if err = a.Remove(n.ID, false); err == nil {
				t.Fatal("unconfirmed removal")
			}
			if err = a.Remove(n.ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(n.Home); !os.IsNotExist(err) {
				t.Fatal(err)
			}
			checkMode(t, other.Home, 0700)
			list, err := a.List()
			if err != nil || len(list) != 1 || list[0].ID != other.ID {
				t.Fatal(list, err)
			}
		})
	}
}
func TestAccountPaths(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(n.Home, alias); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{n.Home, filepath.Join(n.Home, "future"), alias, filepath.Join(alias, "future")} {
		if !IsAccountPath(p) {
			t.Fatal("unprotected", p)
		}
	}
	if IsAccountPath(a.Root + "-other") {
		t.Fatal("sibling protected")
	}
	outside := t.TempDir()
	if err = os.Remove(n.Home); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, n.Home); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Env(n.ID); err == nil {
		t.Fatal("symlink accepted")
	}
	if err = a.Remove(n.ID, true); err == nil {
		t.Fatal("symlink removal accepted")
	}
	if _, err = a.Find("../escape"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if _, err = a.Create("../escape", ""); err == nil {
		t.Fatal("invalid harness accepted")
	}
}
func TestNativeCredentialSymlinkRejected(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "credential")
	if err = os.WriteFile(target, []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(n.Home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Capture(context.Background(), n.ID, ""); err == nil {
		t.Fatal("credential symlink accepted")
	}
	checkMode(t, target, 0644)
}

func TestAccountStorageSymlinksRejected(t *testing.T) {
	for _, name := range []string{"accounts.json", "accounts.lock", "codex"} {
		t.Run(name, func(t *testing.T) {
			a := testAccounts(t)
			if err := os.MkdirAll(a.Root, 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.Chmod(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(a.Root, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Create("codex", ""); err == nil {
				t.Fatal("storage symlink accepted")
			}
			checkMode(t, outside, 0700)
		})
	}
}

func TestNativeCredentialHardlinkRejected(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("codex", "")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "credential")
	if err = os.WriteFile(target, []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(target, filepath.Join(n.Home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Capture(context.Background(), n.ID, ""); err == nil {
		t.Fatal("credential hardlink accepted")
	}
	checkMode(t, target, 0644)
}

func TestClaudeAccountLabelsUseEmail(t *testing.T) {
	a := testAccounts(t)
	login := func(t *testing.T, label, uuid, email string) NativeAccount {
		t.Helper()
		n, err := a.Create("claude-code", label)
		if err != nil {
			t.Fatal(err)
		}
		writeAccountFixture(t, n)
		raw := `{"oauthAccount":{"accountUuid":"` + uuid + `","emailAddress":"` + email + `"}}`
		if err := os.WriteFile(filepath.Join(n.Home, ".claude.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := a.Capture(context.Background(), n.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// A login with no label takes its email as the default label at capture.
	fresh := login(t, "", "aaaaaaaa-bbbb-cccc-dddd-000000000004", "new.person@example.test")
	if fresh.Label != "new.person@example.test" {
		t.Fatalf("new login label = %q; want its email", fresh.Label)
	}
	// Explicit labels remain the owner's choice.
	work := login(t, "Work", "aaaaaaaa-bbbb-cccc-dddd-000000000002", "work@example.test")
	if work.Label != "Work" {
		t.Fatalf("explicit label = %q; want Work", work.Label)
	}
}

// accountMetadataBytes reads the canonical metadata without credential files.
func accountMetadataBytes(t *testing.T, a *Accounts) []byte {
	t.Helper()
	db, err := database.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query("SELECT metadata FROM accounts ORDER BY ord")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, raw...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A native create is a second path beside State.Save, so it must name the
// provider and kind too: a provider lookup would otherwise miss the account.
func TestCreateNamesTheProvider(t *testing.T) {
	a := testAccounts(t)
	n, err := a.Create("codex", "Work")
	if err != nil {
		t.Fatal(err)
	}
	st, err := (&store.Store{Root: a.Root}).Read()
	if err != nil {
		t.Fatal(err)
	}
	entry := st.Find(n.ID)
	if entry == nil || entry.Provider != store.ProviderChatGPT || entry.Kind != store.KindOAuth {
		t.Fatalf("created account = %+v", entry)
	}
}
