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

// saveFixtureCredential writes a saved credential file the migration can
// validate, so a fixture Pi account is signed in rather than a bare row.
func saveFixtureCredential(t *testing.T, dataDir, id, schema string) {
	t.Helper()
	dir := filepath.Join(dataDir, "accounts", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := `{"schema":"` + schema + `","payload":{"token":"` + id + `-sign-in"}}`
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}

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
			// Only a signed-in Pi account can have taken over a sign-in, and its
			// saved credential is what proves the sign-in exists.
			schema := ""
			if row.harness == "pi" {
				schema = "claude-oauth"
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata, credential_schema) VALUES(?, 0, ?, ?)", row.id, metadata, schema); err != nil {
				return err
			}
			if schema != "" {
				saveFixtureCredential(t, dataDir, row.id, schema)
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
	if len(kept) != 4 || kept[0] != "pi-chatgpt" || kept[1] != "pi-muse" || kept[2] != "pi-opencode" || kept[3] != "unknown" {
		t.Fatalf("accounts after migration = %v, want the three Pi accounts and the unmoved unknown-harness account", kept)
	}
	// The retired rows go only because a Pi account already covers their sign-in.
	for _, account := range []string{"codex", "muse", "opencode"} {
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
	// The unmoved account keeps its row and its credential, so the owner can
	// repair it, but no turn can select a harness this release does not run.
	for _, query := range []string{
		"SELECT count(*) FROM accounts WHERE id = 'unknown'",
		"SELECT count(*) FROM account_repo_scopes WHERE account_id = 'unknown'",
		"SELECT count(*) FROM account_limits WHERE account_id = 'unknown'",
	} {
		var left int
		if err := db.QueryRowContext(ctx, query).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 1 {
			t.Fatalf("%q = %d, want the unmoved account kept", query, left)
		}
	}
	for _, query := range []string{
		"SELECT count(*) FROM account_active WHERE account = 'unknown'",
		"SELECT count(*) FROM account_active WHERE harness = 'retired-agent'",
	} {
		var left int
		if err := db.QueryRowContext(ctx, query).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Fatalf("%q = %d, want no selection of a harness this release does not run", query, left)
		}
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
		schema := ""
		if row.harness == "pi" {
			schema = "claude-oauth"
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata, credential_schema) VALUES(?, 0, ?, ?)", row.id, metadata, schema); err != nil {
			t.Fatal(err)
		}
		if schema != "" {
			saveFixtureCredential(t, dataDir, row.id, schema)
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

// D-125: the owner's manual move into Pi is not a precondition. A retired
// harness row whose provider Pi does not cover keeps its row and its saved
// credential, so the owner can repair it or move it; only its selection goes,
// because no turn can select a harness this release does not run.
func TestRegistryMigrationKeepsAnUnmovedRetiredHarness(t *testing.T) {
	s := registryStore(t)
	dataDir := filepath.Dir(s.Root)
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
	if _, err := db.ExecContext(ctx, `CREATE TABLE account_repo_scopes (account_id TEXT NOT NULL, repo TEXT NOT NULL, PRIMARY KEY (account_id, repo));
CREATE TABLE account_limits (account_id TEXT PRIMARY KEY, until INTEGER NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	// One account this release runs, and one retired Codex account whose chatgpt
	// provider has no Pi account yet: the owner never made that move.
	for _, row := range []struct{ id, harness, provider string }{
		{"claude-work", "claude-code", "anthropic"},
		{"codex", "codex", "chatgpt"},
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
	if err := os.MkdirAll(s.credentialDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	saved := `{"schema":"fixture-v1","payload":{"token":"owner-sign-in"}}`
	if err := os.WriteFile(s.credentialPath("codex"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key, value) VALUES('account.registry', '8')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, entry := range state.Entries {
		kept[entry.ID] = true
	}
	if !kept["claude-work"] || !kept["codex"] {
		t.Fatalf("migration kept %v, want the runnable account and the unmoved Codex account", kept)
	}
	// The orphan cleanup of the next write must keep the saved sign-in.
	if err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.credentialPath("codex")); err != nil {
		t.Fatalf("the saved sign-in was discarded: %v", err)
	}
	var selected int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM account_active WHERE harness = 'codex'").Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != 0 {
		t.Fatalf("kept %d selections of a harness this release does not run", selected)
	}
}

// D-125 review: a Pi account the owner has not signed in yet is a slot. It holds
// no identity and no credential, so it must not count as the replacement for an
// unmoved sign-in; otherwise the migration drops the only saved copy of it.
func TestRegistryMigrationKeepsASignInWhenThePiSlotIsUnsigned(t *testing.T) {
	s := registryStore(t)
	dataDir := filepath.Dir(s.Root)
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
	if _, err := db.ExecContext(ctx, `CREATE TABLE account_repo_scopes (account_id TEXT NOT NULL, repo TEXT NOT NULL, PRIMARY KEY (account_id, repo));
CREATE TABLE account_limits (account_id TEXT PRIMARY KEY, until INTEGER NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	// The Pi slot names the provider, waits for a sign-in, and holds no identity.
	slot, err := json.Marshal(Metadata{ID: "pi-chatgpt-slot", Harness: "pi", Provider: ProviderChatGPT, Kind: KindOAuth, NeedsLogin: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES('pi-chatgpt-slot', 0, ?)", slot); err != nil {
		t.Fatal(err)
	}
	moved, err := json.Marshal(Metadata{ID: "codex", Harness: "codex", Provider: ProviderChatGPT, Kind: KindOAuth, Identity: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES('codex', 1, ?)", moved); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.credentialDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.credentialPath("codex"), []byte(`{"schema":"fixture-v1","payload":{"token":"owner-sign-in"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key, value) VALUES('account.registry', '8')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, entry := range state.Entries {
		kept[entry.ID] = true
	}
	if !kept["codex"] || !kept["pi-chatgpt-slot"] {
		t.Fatalf("migration kept %v, want the unsigned Pi slot and the unmoved Codex sign-in", kept)
	}
	if err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.credentialPath("codex")); err != nil {
		t.Fatalf("the unsigned Pi slot discarded the only saved sign-in: %v", err)
	}
}

// D-125 review: a Pi row can carry a schema and an identity while its credential
// file is missing or damaged, which Store.load reads as signed out. Such a row is
// not a replacement, or the migration would delete the only saved sign-in it
// cannot actually stand in for.
func TestRegistryMigrationKeepsASignInWhenThePiCredentialIsMissing(t *testing.T) {
	s := registryStore(t)
	dataDir := filepath.Dir(s.Root)
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
	if _, err := db.ExecContext(ctx, `CREATE TABLE account_repo_scopes (account_id TEXT NOT NULL, repo TEXT NOT NULL, PRIMARY KEY (account_id, repo));
CREATE TABLE account_limits (account_id TEXT PRIMARY KEY, until INTEGER NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	// The Pi row looks signed in: same identity, schema recorded. Its credential
	// file was never written, as a crash between the row and its secret leaves it.
	pi, err := json.Marshal(Metadata{ID: "pi-chatgpt", Harness: "pi", Provider: ProviderChatGPT, Kind: KindOAuth, Identity: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata, credential_schema) VALUES('pi-chatgpt', 0, ?, 'claude-oauth')", pi); err != nil {
		t.Fatal(err)
	}
	codex, err := json.Marshal(Metadata{ID: "codex", Harness: "codex", Provider: ProviderChatGPT, Kind: KindOAuth, Identity: "owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata) VALUES('codex', 1, ?)", codex); err != nil {
		t.Fatal(err)
	}
	saveFixtureCredential(t, dataDir, "codex", "fixture-v1")
	if _, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key, value) VALUES('account.registry', '8')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, entry := range state.Entries {
		kept[entry.ID] = true
	}
	if !kept["codex"] {
		t.Fatalf("migration kept %v, want the Codex sign-in whose Pi account has no saved credential", kept)
	}
	if err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.credentialPath("codex")); err != nil {
		t.Fatalf("a Pi row without a credential discarded the saved sign-in: %v", err)
	}
}
