package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
)

// A saved account names a harness this release runs, unless it names the
// harnesses the owner retired when Pi took over their providers. Version 8
// drops both kinds of row, together with their active marks, repository scopes
// and limits, so no row survives that nothing can sign in or refresh. Pi's own
// accounts for those providers stay.
func TestRegistryMigrationDropsRemovedHarnessRows(t *testing.T) {
	s := registryStore(t)
	dataDir := filepath.Dir(s.Root)
	ctx := context.Background()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	// Build the version-7 registry as the previous release left it: one row for a
	// harness this release runs, one for a harness it does not.
	if err := database.Migrate(db, "account.registry", 7, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(ctx, `CREATE TABLE accounts (
 id TEXT PRIMARY KEY,
 ord INTEGER NOT NULL,
 metadata BLOB NOT NULL CHECK(json_valid(metadata)),
 credential_schema TEXT NOT NULL DEFAULT ''
);
CREATE TABLE accounts_registry (
 id INTEGER PRIMARY KEY CHECK(id = 1),
 version INTEGER NOT NULL,
 opencode_cleaned INTEGER NOT NULL
);`); err != nil {
			return err
		}
		for _, row := range []struct{ id, harness, provider string }{
			{"pi-chatgpt", "pi", "chatgpt"},
			{"pi-muse", "pi", "meta-muse"},
			{"pi-opencode", "pi", "opencode-go"},
			{"codex", "codex", "chatgpt"},
			{"muse", "muse", "meta-muse"},
			{"opencode", "opencode", "opencode-go"},
			{"unknown", "retired-agent", "google"},
		} {
			metadata, err := json.Marshal(map[string]string{"id": row.id, "harness": row.harness, "provider": row.provider, "kind": "oauth"})
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES(?, 0, ?)", row.id, metadata); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, activeSchema); err != nil {
			return err
		}
		for _, active := range []struct{ provider, harness, scope, account string }{
			{"chatgpt", "pi", "", "pi-chatgpt"},
			{"chatgpt", "codex", "", "codex"},
			{"meta-muse", "muse", "room-1", "muse"},
			{"opencode-go", "opencode", "", "opencode"},
			{"google", "retired-agent", "room-1", "unknown"},
		} {
			if _, err := conn.ExecContext(ctx, "INSERT INTO account_active(provider, harness, scope, account) VALUES(?,?,?,?)", active.provider, active.harness, active.scope, active.account); err != nil {
				return err
			}
		}
		// The unknown harness can still own a repository scope and a limit.
		if _, err := conn.ExecContext(ctx, "CREATE TABLE account_repo_scopes (account_id TEXT NOT NULL, repo TEXT NOT NULL, PRIMARY KEY (account_id, repo))"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "CREATE TABLE account_limits (account_id TEXT PRIMARY KEY, until INTEGER NOT NULL)"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO account_repo_scopes(account_id, repo) VALUES('unknown', 'resident-labs/dyna')"); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO account_limits(account_id, until) VALUES('unknown', 1)"); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, "INSERT INTO accounts_registry(id, version, opencode_cleaned) VALUES(1, 7, 1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var kept []string
	rows, err := db.QueryContext(ctx, "SELECT id FROM accounts")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		kept = append(kept, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(kept) != 3 || kept[0] != "pi-chatgpt" || kept[1] != "pi-muse" || kept[2] != "pi-opencode" {
		t.Fatalf("accounts after migration = %v, want only the three Pi provider accounts", kept)
	}
	for _, account := range []string{"codex", "muse", "opencode", "unknown"} {
		for _, query := range []string{
			"SELECT count(*) FROM accounts WHERE id = ?",
			"SELECT count(*) FROM account_active WHERE account = ?",
			"SELECT count(*) FROM account_active WHERE harness = ?",
			"SELECT count(*) FROM account_repo_scopes WHERE account_id = ?",
			"SELECT count(*) FROM account_limits WHERE account_id = ?",
		} {
			var left int
			if err := db.QueryRowContext(ctx, query, account).Scan(&left); err != nil {
				t.Fatal(err)
			}
			if left != 0 {
				t.Fatalf("%q kept %d rows of %q", query, left, account)
			}
		}
	}
	var active int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_active WHERE account = 'pi-chatgpt'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("the selection of the Pi account is gone")
	}
	var left int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'accounts_registry'").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("the retired registry version table stayed")
	}
}

// A Muse or OpenCode row written between the version-8 cleanup and this release
// must not survive: the version-9 step drops it, and only Pi provider accounts
// stay.
func TestRegistryMigrationDropsHarnessRowsWrittenAfterVersionEight(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
	for _, harness := range []string{"muse", "opencode"} {
		if KnownHarness(harness) {
			t.Fatalf("this release still runs %q", harness)
		}
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE accounts (
 id TEXT PRIMARY KEY,
 ord INTEGER NOT NULL,
 metadata BLOB NOT NULL CHECK(json_valid(metadata)),
 credential_schema TEXT NOT NULL DEFAULT ''
);`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, activeSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE account_repo_scopes (account_id TEXT NOT NULL, repo TEXT NOT NULL, PRIMARY KEY (account_id, repo));
CREATE TABLE account_limits (account_id TEXT PRIMARY KEY, until INTEGER NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, harness, provider string }{
		{"pi-chatgpt", "pi", "chatgpt"},
		{"pi-muse", "pi", "meta-muse"},
		{"pi-opencode", "pi", "opencode-go"},
		{"muse", "muse", "meta-muse"},
		{"opencode", "opencode", "opencode-go"},
	} {
		metadata, err := json.Marshal(map[string]string{"id": row.id, "harness": row.harness, "provider": row.provider, "kind": "oauth"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES(?, 0, ?)", row.id, metadata); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO account_active(provider, harness, scope, account) VALUES(?,?,?,?)", row.provider, row.harness, "", row.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO account_repo_scopes(account_id, repo) VALUES('muse', 'resident-labs/dyna')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO account_limits(account_id, until) VALUES('opencode', 1)"); err != nil {
		t.Fatal(err)
	}
	// The database is at the version the previous release left.
	if _, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key, value) VALUES('account.registry', '8')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var kept []string
	rows, err := db.QueryContext(ctx, "SELECT id FROM accounts ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, id)
	}
	rows.Close()
	if want := []string{"pi-chatgpt", "pi-muse", "pi-opencode"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("accounts kept = %v, want %v", kept, want)
	}
	for _, query := range []string{
		"SELECT count(*) FROM account_active WHERE account IN ('muse','opencode')",
		"SELECT count(*) FROM account_repo_scopes WHERE account_id IN ('muse','opencode')",
		"SELECT count(*) FROM account_limits WHERE account_id IN ('muse','opencode')",
	} {
		var left int
		if err := db.QueryRowContext(ctx, query).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Fatalf("%q kept %d rows", query, left)
		}
	}
}
