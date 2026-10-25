package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	statewrite "dyna-agent/internal/state"
)

func registryStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { database.Close(root) })
	return &Store{Root: filepath.Join(root, "accounts")}
}

func saveAccount(t *testing.T, s *Store, token string) Metadata {
	t.Helper()
	var meta Metadata
	if err := s.Update(func(st *State) error {
		var err error
		meta, err = st.Save("fixture", "", "account", "Account", grant(token))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return meta
}

// No credential payload enters dyna.db, its write-ahead log or its backup:
// each lives in the account's own file.
func TestDatabaseAndBackupHoldNoCredentialPayload(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "secret-canary-token")
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.BackUpOnce(db); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Dir(s.Root)
	files, _ := filepath.Glob(filepath.Join(dataDir, "dyna.db*"))
	backups, _ := filepath.Glob(filepath.Join(dataDir, "backups", database.PreMigrationFolder, "*"))
	if len(backups) == 0 {
		t.Fatal("no backup was written")
	}
	for _, path := range append(files, backups...) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("secret-canary-token")) {
			t.Fatalf("%s holds a credential payload", path)
		}
	}
	raw, err := os.ReadFile(s.credentialPath(meta.ID))
	if err != nil || !bytes.Contains(raw, []byte("secret-canary-token")) {
		t.Fatalf("the credential file lacks the payload: %v", err)
	}
}

// The credential folder is 0700 and each credential file 0600.
func TestCredentialFolderAndFileModes(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "mode-token")
	if info, err := os.Stat(s.credentialDir()); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("credential folder mode = %v, %v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(s.credentialPath(meta.ID)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file mode = %v, %v", info.Mode().Perm(), err)
	}
}

// A refresh writes the new secret, then the row. A crash between the two
// leaves the old row, which still asks for a refresh: metadata is never newer
// than its secret.
func TestRefreshCrashBetweenSecretAndRowStillRefreshes(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "old-token")
	if err := s.Update(func(st *State) error {
		st.Find(meta.ID).RefreshPending = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash")
	credentialStep = func(step string) error {
		if step == "updated secrets" {
			return crash
		}
		return nil
	}
	t.Cleanup(func() { credentialStep = func(string) error { return nil } })
	err := s.Update(func(st *State) error {
		e := st.Find(meta.ID)
		e.Credential = grant("new-token")
		e.RefreshPending = false
		return nil
	})
	if !errors.Is(err, crash) {
		t.Fatalf("Update = %v, want the crash", err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	e := st.Find(meta.ID)
	if e == nil || !e.RefreshPending {
		t.Fatalf("after the crash, the account = %+v; want a refresh still pending", e)
	}
}

// A row whose credential file is missing reads as signed out.
func TestRowWithoutItsSecretReadsAsSignedOut(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	if err := os.Remove(s.credentialPath(meta.ID)); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if e := st.Find(meta.ID); e == nil || !e.NeedsLogin || e.Credential.Schema != "" {
		t.Fatalf("account without its secret = %+v; want signed out", e)
	}
}

// Removing an account deletes its row, then its credential file.
func TestRemoveDeletesTheSecret(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	if err := s.Update(func(st *State) error {
		st.Remove(meta.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.credentialPath(meta.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the credential file stayed after removal: %v", err)
	}
	if st, _ := s.Read(); st.Find(meta.ID) != nil {
		t.Fatal("the row stayed after removal")
	}
}

// Only a committed change to the saved accounts publishes the accounts topic.
func TestSaveRegistryPublishesOnlyCommittedChanges(t *testing.T) {
	s := registryStore(t)
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	var topics []string
	statewrite.For(db).SetPublish(func(ev hub.Event) { topics = append(topics, ev.Topic) })
	t.Cleanup(func() { statewrite.For(db).SetPublish(nil) })
	meta := saveAccount(t, s, "first-token")
	steps := []struct {
		name string
		fn   func(*State) error
		want int
	}{
		{"empty save", func(*State) error { return nil }, 1},
		{"label change", func(st *State) error { st.Find(meta.ID).Label = "renamed"; return nil }, 2},
		{"rotated credential", func(st *State) error {
			_, err := st.Save("fixture", "", "account", "", grant("second-token"))
			return err
		}, 3},
		{"failed callback", func(st *State) error { st.Find(meta.ID).Label = "lost"; return errors.New("abort") }, 3},
		{"removal", func(st *State) error { st.Remove(meta.ID); return nil }, 4},
	}
	for _, step := range steps {
		_ = s.Update(step.fn)
		if len(topics) != step.want {
			t.Fatalf("after %s: %d events, want %d", step.name, len(topics), step.want)
		}
	}
	for _, topic := range topics {
		if topic != Topic {
			t.Fatalf("topic %q, want %q", topic, Topic)
		}
	}
}

// The refresh pass writes quietly: its own checkpoints must not wake the
// pass again before its retry delay.
func TestQuietStoreChangesPublishNothing(t *testing.T) {
	s := registryStore(t)
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	var topics []string
	statewrite.For(db).SetPublish(func(ev hub.Event) { topics = append(topics, ev.Topic) })
	t.Cleanup(func() { statewrite.For(db).SetPublish(nil) })
	meta := saveAccount(t, s, "first-token")
	rename := func(s *Store, label string) {
		t.Helper()
		if err := s.Update(func(st *State) error { st.Find(meta.ID).Label = label; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	rename(&Store{Root: s.Root, Quiet: true}, "pass write")
	if len(topics) != 1 {
		t.Fatalf("a quiet change published %v", topics[1:])
	}
	rename(s, "external write")
	if len(topics) != 2 {
		t.Fatalf("an external change published %d events, want 1 more", len(topics)-1)
	}
}

func TestRegistryWritesOnlyChangedRows(t *testing.T) {
	s := registryStore(t)
	first := saveAccount(t, s, "first")
	var second Metadata
	if err := s.Update(func(st *State) error {
		var err error
		second, err = st.Save("fixture", "", "second", "Second", grant("second"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"CREATE TABLE writes (operation TEXT, id TEXT)",
		"CREATE TRIGGER track_insert AFTER INSERT ON accounts BEGIN INSERT INTO writes VALUES ('insert', new.id); END",
		"CREATE TRIGGER track_update AFTER UPDATE ON accounts BEGIN INSERT INTO writes VALUES ('update', new.id); END",
		"CREATE TRIGGER track_delete AFTER DELETE ON accounts BEGIN INSERT INTO writes VALUES ('delete', old.id); END",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	steps := []struct {
		name   string
		change func(*State) error
		want   []string
	}{
		{"unchanged", func(*State) error { return nil }, nil},
		{"rename", func(st *State) error { st.Find(second.ID).Label = "Renamed"; return nil }, []string{"update " + second.ID}},
		{"remove and reorder", func(st *State) error { st.Remove(first.ID); return nil }, []string{"update " + second.ID, "delete " + first.ID}},
	}
	for _, step := range steps {
		if err := s.Update(step.change); err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query("SELECT operation || ' ' || id FROM writes ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			got = append(got, line)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, step.want) {
			t.Fatalf("%s wrote %v, want %v", step.name, got, step.want)
		}
		if _, err := db.Exec("DELETE FROM writes"); err != nil {
			t.Fatal(err)
		}
	}
}
