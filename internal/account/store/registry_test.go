package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// The previous release reads credentials from accounts.json, so its copy
// holds each account's credential.
func TestRollbackCopyHoldsTheCredentials(t *testing.T) {
	s := registryStore(t)
	saveAccount(t, s, "copy-canary-token")
	raw, err := os.ReadFile(s.registryPath())
	if err != nil || !bytes.Contains(raw, []byte("copy-canary-token")) {
		t.Fatalf("the rollback copy lacks the credential: %v", err)
	}
	if info, err := os.Stat(s.registryPath()); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("rollback copy mode = %v, %v", info.Mode().Perm(), err)
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

// A usage observation does not rewrite the rollback copy, which holds every
// secret; a change the previous release acts on does.
func TestCopyIsRewrittenOnlyWhenTheEarlierReleaseActsOnTheChange(t *testing.T) {
	s := registryStore(t)
	meta := saveAccount(t, s, "token")
	first, err := os.ReadFile(s.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error {
		st.Find(meta.ID).Observation = &Observation{UsageError: "observed", Windows: []Window{}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if same, _ := os.ReadFile(s.registryPath()); !bytes.Equal(same, first) {
		t.Fatal("an observation rewrote the copy")
	}
	if st, _ := s.Read(); st.Find(meta.ID).Observation == nil {
		t.Fatal("the observation was not saved in the row")
	}
	if err := s.Update(func(st *State) error {
		st.Find(meta.ID).NeedsLogin = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(s.registryPath()); !strings.Contains(string(after), `"needsLogin": true`) {
		t.Fatal("a NeedsLogin change did not rewrite the copy")
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

// An old binary's registry without account B leaves no B credential file
// after the import.
func TestImportDropsTheSecretOfAnAccountTheOldFileRemoved(t *testing.T) {
	s := registryStore(t)
	a := saveAccount(t, s, "a-token")
	var b Metadata
	if err := s.Update(func(st *State) error {
		var err error
		b, err = st.Save("fixture", "", "other", "Other", grant("b-token"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.Remove(b.ID)
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// The previous release rewrites accounts.json without account B.
	tmp := s.registryPath() + ".old"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, s.registryPath()); err != nil {
		t.Fatal(err)
	}
	after, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if after.Find(b.ID) != nil || after.Find(a.ID) == nil {
		t.Fatalf("after the import, accounts = %+v", after.Entries)
	}
	if _, err := os.Stat(s.credentialPath(b.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed account's credential file stayed: %v", err)
	}
	if _, err := os.Stat(s.credentialPath(a.ID)); err != nil {
		t.Fatalf("the kept account's credential file is gone: %v", err)
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
