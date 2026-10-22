package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/database"
	"dyna-agent/internal/harness/preferences"
	statewrite "dyna-agent/internal/state"
)

func TestActiveSelectionImportsOnceAndDoesNotResurrect(t *testing.T) {
	s := registryStore(t)
	dataDir := filepath.Dir(s.Root)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "account.registry", 2, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), registrySchema+activeSchema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := statewrite.MigrateMirrors(dataDir); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	raw := []byte(`{"id":"` + id + `"}`)
	for _, h := range []string{"fixture", "cleared-final", "cleared-pending"} {
		if err := os.MkdirAll(filepath.Dir(s.activePath(h)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.activePath(h), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{"cleared-final", "cleared-pending"} {
		version, pending := "prior-copy", ""
		if h == "cleared-pending" {
			version, pending = "", "prior-copy"
		}
		if _, err := db.Exec("INSERT INTO state_mirrors(key,version,pending) VALUES(?,?,?)", activeKey(h), version, pending); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow("SELECT account FROM account_active WHERE harness = ?", "fixture").Scan(&got); err != nil || got != id {
		t.Fatalf("eager selection = %q, %v", got, err)
	}
	for _, h := range []string{"cleared-final", "cleared-pending"} {
		if got, err := s.Active(h); err != nil || got != "" {
			t.Fatalf("%s restored selection %q: %v", h, got, err)
		}
	}
	if err := s.SetActive("fixture", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(s.activePath("fixture")); err != nil || string(got) != string(raw) {
		t.Fatalf("SetActive changed retained bytes: %q, %v", got, err)
	}
	if err := os.Remove(s.activePath("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.activePath("fixture"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Active("fixture"); err != nil || got != "" {
		t.Fatalf("cleared selection = %q, %v", got, err)
	}
	if err := db.QueryRow("SELECT account FROM account_active WHERE harness = ?", "fixture").Scan(&got); err != nil || got != "" {
		t.Fatalf("empty selection row = %q, %v", got, err)
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
	if err := s.SetActive("fixture", meta.ID); err != nil {
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
