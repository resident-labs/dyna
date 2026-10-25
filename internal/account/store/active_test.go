package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/database"
	"dyna-agent/internal/harness/preferences"
)

// legacyActiveSchema is the per-harness selection table of the previous
// release, which the migration replaces.
const legacyActiveSchema = `CREATE TABLE account_active (
 harness TEXT PRIMARY KEY,
 account TEXT NOT NULL
);`

// A registry from the previous release moves once: each row names its provider
// and kind, and one global row per provider replaces the per-harness rows.
func TestMigrationMovesHarnessSelectionsToProviders(t *testing.T) {
	s := registryStore(t)
	dataDir := filepath.Dir(s.Root)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "account.registry", 4, func(conn *sql.Conn, _ int) error {
		ctx := context.Background()
		if _, err := conn.ExecContext(ctx, registrySchema+legacyActiveSchema); err != nil {
			return err
		}
		rows := []struct{ id, harness string }{
			{"11111111111111111111111111111111", "claude-code"},
			{"22222222222222222222222222222222", "grok"},
		}
		for i, row := range rows {
			meta := `{"id":"` + row.id + `","harness":"` + row.harness + `","identity":"` + row.harness + `:one","label":"One","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`
			if _, err := conn.ExecContext(ctx, "INSERT INTO accounts(id, ord, metadata, credential_schema) VALUES (?, ?, ?, '')", row.id, i, meta); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, "INSERT INTO account_active(harness, account) VALUES (?, ?)", row.harness, row.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		provider Provider
		kind     Kind
	}{
		"11111111111111111111111111111111": {ProviderAnthropic, KindOAuth},
		"22222222222222222222222222222222": {ProviderXAI, KindOAuth},
	}
	if len(st.Entries) != len(want) {
		t.Fatalf("migrated %d accounts, want %d", len(st.Entries), len(want))
	}
	for _, entry := range st.Entries {
		if entry.Provider != want[entry.ID].provider || entry.Kind != want[entry.ID].kind {
			t.Fatalf("account %s moved to (%q, %q)", entry.ID, entry.Provider, entry.Kind)
		}
	}
	// Each selection keeps the harness of the account it names.
	for _, row := range []struct {
		provider Provider
		harness  string
		id       string
	}{
		{ProviderAnthropic, "claude-code", "11111111111111111111111111111111"},
		{ProviderXAI, "grok", "22222222222222222222222222222222"},
	} {
		if got, err := s.Active(row.provider, row.harness, ""); err != nil || got != row.id {
			t.Fatalf("active %s for %s = %q, %v", row.provider, row.harness, got, err)
		}
		if got, err := s.Active(row.provider, "pi", ""); err != nil || got != "" {
			t.Fatalf("pi took %s's selection: %q, %v", row.harness, got, err)
		}
	}
	var harnessTable int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'account_active_harness'").Scan(&harnessTable); err != nil || harnessTable != 0 {
		t.Fatalf("the per-harness table stayed: %d, %v", harnessTable, err)
	}
}

// A room row overrides the global row for its provider.
func TestActiveForPrefersTheRoomRow(t *testing.T) {
	s := registryStore(t)
	shared := saveAccount(t, s, "shared")
	room := saveAccount(t, s, "room")
	provider, _ := ProviderFor("fixture", "")
	if err := s.SetActive(provider, "pi", "", shared.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ActiveFor(provider, "pi", "room-1"); err != nil || got != shared.ID {
		t.Fatalf("room without a row = %q, %v", got, err)
	}
	if err := s.SetActive(provider, "pi", "room-1", room.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ActiveFor(provider, "pi", "room-1"); err != nil || got != room.ID {
		t.Fatalf("room row = %q, %v", got, err)
	}
	if got, err := s.ActiveFor(provider, "pi", "room-2"); err != nil || got != shared.ID {
		t.Fatalf("another room = %q, %v", got, err)
	}
	if err := s.SetActive(provider, "pi", "room-1", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ActiveFor(provider, "pi", "room-1"); err != nil || got != "" {
		t.Fatalf("cleared room row = %q, %v", got, err)
	}
}

// Removing an account deletes, in its own write, its selection, its repo
// scope and limit rows (reposcope) and its preference row.
func TestRemovingAnAccountDeletesItsLinkedRows(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	dataDir := filepath.Dir(s.Root)
	scopes, err := reposcope.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := scopes.Set(meta.ID, []string{"github.com/org/repo"}); err != nil {
		t.Fatal(err)
	}
	if err := scopes.MarkLimited(meta.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := preferences.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO harness_preferences(account, ref) VALUES (?, '{}')", meta.ID); err != nil {
		t.Fatal(err)
	}
	provider, _ := ProviderFor("fixture", "")
	if err := s.SetActive(provider, "pi", "", meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error {
		st.Remove(meta.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM account_active WHERE account = ?",
		"SELECT COUNT(*) FROM account_repo_scopes WHERE account_id = ?",
		"SELECT COUNT(*) FROM account_limits WHERE account_id = ?",
		"SELECT COUNT(*) FROM harness_preferences WHERE account = ?",
	} {
		var n int
		if err := db.QueryRowContext(context.Background(), query, meta.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%q found %d rows of the removed account", query, n)
		}
	}
}

// A removal in a data folder where the other stores have not migrated yet
// skips their tables.
func TestRemovingAnAccountBeforeOtherStoresMigrate(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	if err := s.Update(func(st *State) error {
		st.Remove(meta.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A secret left by a crash during a removal is dropped at the next
// transaction.
func TestTransactionDropsAnOrphanSecret(t *testing.T) {
	s := registryStore(t)
	saveAccount(t, s, "token")
	orphan := s.credentialPath("00000000000000000000000000000000")
	if err := os.WriteFile(orphan, []byte(`{"schema":"fixture-v1","payload":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the orphan secret stayed: %v", err)
	}
}
