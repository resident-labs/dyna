package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
)

// saveCredential writes one saved credential file, so a fixture account is
// signed in rather than a bare row.
func saveCredential(t *testing.T, dataDir, id string, c Credential) {
	t.Helper()
	dir := filepath.Join(dataDir, "accounts", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Version 9 retires the selections of harnesses this release does not run, and
// never deletes a saved account: a retired row can be the only copy of an owner's
// sign-in, so it stays, with its scope, limit and credential, until the importer
// moves that exact sign-in or the owner removes it (D-125). Pi's own accounts stay
// selected.
func TestRegistryMigrationKeepsRetiredHarnessRowsAndClearsSelections(t *testing.T) {
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
		for _, row := range []struct{ id, harness, provider, identity string }{
			{"pi-chatgpt", "pi", "chatgpt", "owner@example.test"},
			{"pi-muse", "pi", "meta-muse", "owner@example.test"},
			{"pi-opencode", "pi", "opencode-go", "owner@example.test"},
			{"codex", "codex", "chatgpt", "owner@example.test"},
			{"muse", "muse", "meta-muse", "owner@example.test"},
			{"opencode", "opencode", "opencode-go", "owner@example.test"},
			{"unknown", "retired-agent", "google", "someone@example.test"},
		} {
			metadata, err := json.Marshal(map[string]string{"id": row.id, "harness": row.harness, "provider": row.provider, "kind": "oauth", "identity": row.identity})
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
	present := map[string]bool{}
	for _, id := range kept {
		present[id] = true
	}
	for _, id := range []string{"pi-chatgpt", "pi-muse", "pi-opencode", "codex", "muse", "opencode", "unknown"} {
		if !present[id] {
			t.Fatalf("accounts after migration = %v, want %q kept", kept, id)
		}
	}
	// Every retired row keeps its account, scope and limit; only its selection
	// goes, because no turn can select a harness this release does not run.
	for _, account := range []string{"codex", "muse", "opencode", "unknown"} {
		var rows, selected int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE id = ?", account).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_active WHERE account = ?", account).Scan(&selected); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || selected != 0 {
			t.Fatalf("%q kept %d rows and %d selections, want the row kept and no selection", account, rows, selected)
		}
	}
	for _, harness := range []string{"codex", "muse", "opencode", "retired-agent"} {
		var selected int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_active WHERE harness = ?", harness).Scan(&selected); err != nil {
			t.Fatal(err)
		}
		if selected != 0 {
			t.Fatalf("%d selections of the retired harness %q stayed", selected, harness)
		}
	}
	var scoped int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_repo_scopes WHERE account_id = 'unknown'").Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if scoped != 1 {
		t.Fatalf("the retired account lost its repository scope")
	}
	var active int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_active WHERE account = 'pi-chatgpt'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("the selection of the Pi account is gone")
	}
	// The saved sign-in of the retired harness survives the next write too.
	saveCredential(t, dataDir, "codex", Credential{Schema: "fixture-v1", Payload: json.RawMessage(`{"token":"owner-sign-in"}`)})
	if err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.credentialPath("codex")); err != nil {
		t.Fatalf("the retired account lost its saved sign-in: %v", err)
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
// also stays: version 9 never deletes a saved account, so the row survives while
// its selection, scope and limit are kept for the owner to repair or remove.
func TestRegistryMigrationKeepsHarnessRowsWrittenAfterVersionEight(t *testing.T) {
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
	for _, row := range []struct{ id, harness, provider, identity string }{
		{"pi-chatgpt", "pi", "chatgpt", "owner@example.test"},
		{"pi-muse", "pi", "meta-muse", "owner@example.test"},
		{"pi-opencode", "pi", "opencode-go", "owner@example.test"},
		{"muse", "muse", "meta-muse", "owner@example.test"},
		{"opencode", "opencode", "opencode-go", "owner@example.test"},
	} {
		metadata, err := json.Marshal(map[string]string{"id": row.id, "harness": row.harness, "provider": row.provider, "kind": "oauth", "identity": row.identity})
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
	if want := []string{"muse", "opencode", "pi-chatgpt", "pi-muse", "pi-opencode"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("accounts kept = %v, want %v", kept, want)
	}
	for _, query := range []string{
		"SELECT count(*) FROM account_active WHERE account IN ('muse','opencode')",
		"SELECT count(*) FROM account_active WHERE harness IN ('muse','opencode')",
	} {
		var left int
		if err := db.QueryRowContext(ctx, query).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Fatalf("%q kept %d rows, want only the selection cleared", query, left)
		}
	}
	for _, query := range []string{
		"SELECT count(*) FROM account_repo_scopes WHERE account_id IN ('muse','opencode')",
		"SELECT count(*) FROM account_limits WHERE account_id IN ('muse','opencode')",
	} {
		var left int
		if err := db.QueryRowContext(ctx, query).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 1 {
			t.Fatalf("%q = %d, want the retired account's own row kept", query, left)
		}
	}
}

// Version 9 must not re-read the current selection table as the legacy one: a
// database at version 8 keeps its global choice, its room choices and its
// explicit empty choice.
func TestRegistryMigrationKeepsSelectionsAtVersionEight(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
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
	for _, row := range []struct{ id, provider string }{
		{"pi-chatgpt", "chatgpt"},
		{"pi-chatgpt-work", "chatgpt"},
	} {
		metadata, err := json.Marshal(map[string]string{"id": row.id, "harness": "pi", "provider": row.provider, "kind": "oauth"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES(?, 0, ?)", row.id, metadata); err != nil {
			t.Fatal(err)
		}
	}
	for _, active := range []struct{ scope, account string }{
		{"", "pi-chatgpt"},
		{"room-a", "pi-chatgpt-work"},
		{"room-b", ""},
	} {
		if _, err := db.ExecContext(ctx, "INSERT INTO account_active(provider, harness, scope, account) VALUES('chatgpt','pi',?,?)", active.scope, active.account); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key, value) VALUES('account.registry', '8')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ scope, account string }{
		{"", "pi-chatgpt"},
		{"room-a", "pi-chatgpt-work"},
		{"room-b", ""},
	} {
		var account string
		if err := db.QueryRowContext(ctx, "SELECT account FROM account_active WHERE provider = 'chatgpt' AND harness = 'pi' AND scope = ?", want.scope).Scan(&account); err != nil {
			t.Fatalf("scope %q: %v", want.scope, err)
		}
		if account != want.account {
			t.Fatalf("scope %q holds %q, want %q", want.scope, account, want.account)
		}
	}
}
