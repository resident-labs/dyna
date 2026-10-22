package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/database"
	"dyna-agent/internal/harness/preferences"
)

// The selection lives in its row; .active.json is the previous release's
// 0600 copy.
func TestSelectionLivesInItsRowWithA0600Copy(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	if err := s.SetActive("fixture", meta.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.activePath("fixture"))
	if err != nil || !strings.Contains(string(raw), meta.ID) {
		t.Fatalf("selection copy = %q, %v", raw, err)
	}
	if info, err := os.Stat(s.activePath("fixture")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("selection copy mode = %v, %v", info.Mode().Perm(), err)
	}
	if err := os.Remove(s.activePath("fixture")); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Active("fixture"); err != nil || id != meta.ID {
		t.Fatalf("without the copy, Active = %q, %v", id, err)
	}
	if err := s.SetActive("fixture", ""); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Active("fixture"); err != nil || id != "" {
		t.Fatalf("after clearing, Active = %q, %v", id, err)
	}
}

// A selection that the previous release wrote is imported on read; a
// damaged one is skipped and the row stays.
func TestOldBinarySelectionIsImportedAndADamagedOneSkipped(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	if err := os.MkdirAll(filepath.Dir(s.activePath("fixture")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.activePath("fixture"), []byte(`{"id":"`+meta.ID+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Active("fixture"); err != nil || id != meta.ID {
		t.Fatalf("old binary's selection, Active = %q, %v", id, err)
	}
	if err := os.Remove(s.activePath("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.activePath("fixture"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Active("fixture"); err != nil || id != meta.ID {
		t.Fatalf("after a damaged selection, Active = %q, %v", id, err)
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
