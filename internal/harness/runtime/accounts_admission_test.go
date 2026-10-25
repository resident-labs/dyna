package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func preparedAccountFixture(t *testing.T) (*Accounts, NativeAccount) {
	t.Helper()
	a := testAccounts(t)
	n := signedClaude(t, a, "prepared", "aaaaaaaa-bbbb-cccc-dddd-000000000001")
	if err := a.setActive("claude-code", n.ID); err != nil {
		t.Fatal(err)
	}
	return a, n
}

func TestCheckPreparedAccount(t *testing.T) {
	a, n := preparedAccountFixture(t)
	for _, harness := range []string{"claude", "claude-code"} {
		got, err := a.CheckPreparedAccount(harness, n.ID, "")
		if err != nil || got.ID != n.ID || !got.Active || got.Status != "ready" {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
	for _, harness := range []string{"codex", "unknown"} {
		if _, err := a.CheckPreparedAccount(harness, n.ID, ""); err == nil {
			t.Fatalf("accepted %s", harness)
		}
	}
	if _, err := a.CheckPreparedAccount("claude", "different-account", ""); err == nil {
		t.Fatal("accepted a different prepared account")
	}
	if err := a.SetRepos(n.ID, []string{workRepo}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CheckPreparedAccount("claude", n.ID, ""); !errors.Is(err, errScopeRefused) {
		t.Fatalf("no-origin error = %v", err)
	}
	if _, err := a.CheckPreparedAccount("claude", n.ID, workRepo); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPreparedAccountRejectsChangedLogin(t *testing.T) {
	a, n := preparedAccountFixture(t)
	login, err := readAccountLogin(n.Home)
	if err != nil {
		t.Fatal(err)
	}
	login.Identity = "different identity"
	if err := writePrivateAccountJSON(filepath.Join(n.Home, ".login-pending.json"), login); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CheckPreparedAccount("claude", n.ID, ""); err == nil {
		t.Fatal("accepted a changed login identity")
	}
}

func TestCheckPreparedAccountRejectsSignedOut(t *testing.T) {
	a, n := preparedAccountFixture(t)
	if err := a.transaction(func(s *store.State) error { s.Find(n.ID).NeedsLogin = true; return nil }, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CheckPreparedAccount("claude", n.ID, ""); !errors.Is(err, native.ErrNotSignedIn) {
		t.Fatalf("signed-out error = %v", err)
	}
}

func TestRecheckPreparedAccount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		query   string
		args    func(NativeAccount) []any
		wantErr bool
	}{
		{name: "current account"},
		{name: "active changed", query: "UPDATE account_active SET account = ? WHERE provider = ?", args: func(n NativeAccount) []any {
			return []any{"00000000000000000000000000000000", accountProvider(n.Harness)}
		}, wantErr: true},
		{name: "active cleared", query: "DELETE FROM account_active", wantErr: true},
		{name: "identity changed", query: "UPDATE accounts SET metadata = json_set(metadata, '$.identity', 'other')", wantErr: true},
		{name: "signed out", query: "UPDATE accounts SET metadata = json_set(metadata, '$.needsLogin', json('true'))", wantErr: true},
		{name: "deleted", query: "DELETE FROM accounts", wantErr: true},
		{name: "harness changed", query: "UPDATE accounts SET metadata = json_set(metadata, '$.harness', 'codex')", wantErr: true},
		{name: "home changed", query: "UPDATE accounts SET metadata = json_set(metadata, '$.nativeHome', '/different')", wantErr: true},
		{name: "integration changed", query: "UPDATE accounts SET metadata = json_set(metadata, '$.integration', 'different')", wantErr: true},
		{name: "account scope changed", query: "INSERT INTO account_repo_scopes(account_id, repo) VALUES (?, ?)", args: func(n NativeAccount) []any { return []any{n.ID, workRepo} }, wantErr: true},
		{name: "scope changed but still allows repo", query: "INSERT INTO account_repo_scopes(account_id, repo) VALUES (?, ?)", args: func(n NativeAccount) []any { return []any{n.ID, personalRepo} }, wantErr: true},
		{name: "repo protected by another account", query: "INSERT INTO account_repo_scopes(account_id, repo) VALUES (?, ?)", args: func(NativeAccount) []any { return []any{"another-account", personalRepo} }, wantErr: true},
		{name: "unrelated label changed", query: "UPDATE accounts SET metadata = json_set(metadata, '$.label', 'new label')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, n := preparedAccountFixture(t)
			expected, err := a.CheckPreparedAccount("claude", n.ID, personalRepo)
			if err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(filepath.Dir(a.Root))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
				if tc.query != "" {
					var args []any
					if tc.args != nil {
						args = tc.args(n)
					}
					if _, err := conn.ExecContext(ctx, tc.query, args...); err != nil {
						return err
					}
				}
				err := a.RecheckPreparedAccount(ctx, conn, expected, personalRepo)
				if (err != nil) != tc.wantErr {
					t.Fatalf("recheck error = %v, want error %v", err, tc.wantErr)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecheckPreparedAccountUsesOnlySQL(t *testing.T) {
	a, n := preparedAccountFixture(t)
	expected, err := a.CheckPreparedAccount("claude", n.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(a.Root))
	if err != nil {
		t.Fatal(err)
	}
	// Remove all account files. The write recheck must use its SQL snapshot only.
	if err := os.RemoveAll(a.Root); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if err := a.RecheckPreparedAccount(ctx, conn, expected, ""); err != nil {
			return err
		}
		if err := a.RecheckPreparedAccount(ctx, conn, expected, workRepo); err == nil {
			t.Fatal("accepted an unchecked repository")
		}
		raw, err := json.Marshal(expected)
		if err != nil {
			return err
		}
		var copied NativeAccount
		if err := json.Unmarshal(raw, &copied); err != nil {
			return err
		}
		if err := a.RecheckPreparedAccount(ctx, conn, copied, ""); err == nil {
			t.Fatal("accepted an account without a checked snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
