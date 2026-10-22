package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func grant(token string) Credential {
	raw, _ := json.Marshal(map[string]string{"token": token})
	return Credential{Schema: "fixture-v1", Payload: raw}
}

func TestSaveRotatedGrantRetainsIDAndLabel(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "accounts")}
	var original Metadata
	if err := s.Update(func(st *State) error {
		var err error
		original, err = st.Save("codex", "chatgpt", "user-1", "Personal", grant("first"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *State) error {
		meta, err := st.Save("codex", "chatgpt", "user-1", "Provider name", grant("rotated"))
		if meta.ID != original.ID || meta.Label != "Personal" || !meta.CreatedAt.Equal(original.CreatedAt) {
			t.Fatal("saved account identity changed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil || len(st.Entries) != 1 || !strings.Contains(string(st.Entries[0].Credential.Payload), "rotated") {
		t.Fatalf("grant not retained: %v", err)
	}
	raw, err := json.Marshal(st.Entries[0].Metadata)
	if err != nil || strings.Contains(string(raw), "rotated") || strings.Contains(string(raw), "credential") {
		t.Fatal("metadata contains credentials")
	}
	info, err := os.Stat(filepath.Join(s.Root, "accounts.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential file permissions")
	}
}

func TestReadDoesNotCreateAndFailedUpdateDoesNotReplace(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "accounts")}
	if _, err := s.Read(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatal("read created store")
	}
	if err := s.Update(func(st *State) error { _, err := st.Save("fixture", "", "a", "Test", grant("original")); return err }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(s.Root, "accounts.json"))
	err := s.Update(func(st *State) error { st.Entries = nil; return errors.New("activation failed") })
	after, _ := os.ReadFile(filepath.Join(s.Root, "accounts.json"))
	if err == nil || string(before) != string(after) {
		t.Fatal("failed transaction changed saved state")
	}
	if err := os.WriteFile(filepath.Join(s.Root, "accounts.json"), []byte(`{"version":99,"secret":"must-not-leak"}`), 0600); err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(st *State) error { t.Fatal("invalid state reached update"); return nil })
	if err == nil || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatal("invalid state was accepted or leaked")
	}
}

func TestSameIdentityInDifferentIntegrationsStaysSeparate(t *testing.T) {
	st := State{Version: 1}
	a, err := st.Save("fixture", "one", "identity", "One", grant("one"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Save("fixture", "two", "identity", "Two", grant("two"))
	if err != nil || a.ID == b.ID || len(st.Entries) != 2 {
		t.Fatal("integration identities merged")
	}
	if !st.Remove(a.ID) || st.Find(a.ID) != nil || st.Find(b.ID) == nil {
		t.Fatal("remove affected another integration")
	}
}

func TestCheckpointRetainsRotatedGrantWhenActivationFails(t *testing.T) {
	s := Store{Root: t.TempDir()}
	err := s.Transaction(func(st *State, checkpoint func() error) error {
		if _, err := st.Save("fixture", "", "a", "Account", grant("rotated")); err != nil {
			return err
		}
		if err := checkpoint(); err != nil {
			return err
		}
		st.Entries = nil // uncommitted changes must not replace the checkpoint
		return errors.New("activation failed")
	})
	st, readErr := s.Read()
	if err == nil || readErr != nil || len(st.Entries) != 1 || !strings.Contains(string(st.Entries[0].Credential.Payload), "rotated") {
		t.Fatalf("checkpoint lost after failed activation: %v, %v", err, readErr)
	}
	info, err := os.Stat(s.Root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("existing account directory permissions were not corrected")
	}
}

// An obstructed rollback copy fails the update without losing the grant:
// the rows and the account's 0600 credential file hold it, and the error
// never contains it.
func TestObstructedCopyKeepsTheGrant(t *testing.T) {
	s := Store{Root: t.TempDir()}
	err := s.Update(func(st *State) error {
		if _, err := st.Save("fixture", "", "account", "Account", grant("latest-private-grant")); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(s.Root, "accounts.json"), 0700)
	})
	if err == nil {
		t.Fatal("obstructed copy succeeded")
	}
	if strings.Contains(err.Error(), "latest-private-grant") {
		t.Fatal("credential appeared in the error")
	}
	st, readErr := s.Read()
	if readErr == nil {
		t.Fatal("a directory in place of the registry copy read as accounts")
	}
	if err := os.Remove(filepath.Join(s.Root, "accounts.json")); err != nil {
		t.Fatal(err)
	}
	st, readErr = s.Read()
	if readErr != nil || len(st.Entries) != 1 || !strings.Contains(string(st.Entries[0].Credential.Payload), "latest-private-grant") {
		t.Fatalf("grant lost after an obstructed copy: %+v, %v", st, readErr)
	}
}

func TestObservationRoundTripRetainsCheckpointedUsage(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	used := 72.0
	observed := time.Now().UTC().Truncate(time.Second)
	until := observed.Add(15 * time.Minute)
	var id string
	err := store.Update(func(st *State) error {
		meta, err := st.Save("codex", "", "fixture:account", "Saved", Credential{Schema: "fixture@1", Payload: json.RawMessage(`{}`)})
		if err != nil {
			return err
		}
		id = meta.ID
		st.Find(id).Observation = &Observation{Plan: &Plan{ID: "pro", Label: "Pro"}, Windows: []Window{{ID: "weekly", UsedPercent: &used, ResetsAt: until.Format(time.RFC3339)}}, ObservedAt: observed, RateLimitedUntil: &until, Failures: 2, Unavailable: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(st *State) error {
		_, err := st.Save("codex", "", "fixture:account", "", Credential{Schema: "fixture@1", Payload: json.RawMessage(`{"rotated":true}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	o := st.Find(id).Observation
	if o == nil || o.Plan.ID != "pro" || len(o.Windows) != 1 || *o.Windows[0].UsedPercent != 72 || !o.ObservedAt.Equal(observed) || !o.RateLimitedUntil.Equal(until) || o.Failures != 2 || !o.Unavailable {
		t.Fatal("observation lost during credential checkpoint")
	}
}

func TestLegacyOpenCodeProviderCleanupPreservesNativeCredentials(t *testing.T) {
	root := t.TempDir()
	store := &Store{Root: filepath.Join(root, "dyna")}
	native := filepath.Join(root, "opencode-v2.db")
	original := []byte("native Go, Zen, OpenAI and xAI credentials")
	if err := os.WriteFile(native, original, 0600); err != nil {
		t.Fatal(err)
	}
	st := State{Version: 1}
	for _, integration := range []string{"opencode-go", "opencode", "openai", "xai", "anthropic"} {
		if _, err := st.Save("opencode", integration, integration, "OpenCode account", grant("reference")); err != nil {
			t.Fatal(err)
		}
	}
	st.Entries = append(st.Entries, Entry{Metadata: Metadata{ID: "private", Harness: "opencode", NativeHome: filepath.Join(root, "private")}, Credential: grant("private")})
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	// The registry as the previous release wrote it.
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root, "accounts.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(store.Root, "accounts.json"))
	visible, err := store.Read()
	if err != nil || len(visible.Entries) != 3 {
		t.Fatalf("read filter: %+v, %v", visible, err)
	}
	unchanged, _ := os.ReadFile(filepath.Join(store.Root, "accounts.json"))
	if string(unchanged) != string(before) {
		t.Fatal("read persisted cleanup")
	}
	for range 2 {
		if err := store.Transaction(func(st *State, _ func() error) error {
			if !st.OpenCodeProvidersCleaned || len(st.Entries) != 3 {
				t.Fatal("cleanup did not run")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ = os.ReadFile(filepath.Join(store.Root, "accounts.json"))
	var persisted State
	if json.Unmarshal(raw, &persisted) != nil || !persisted.OpenCodeProvidersCleaned || len(persisted.Entries) != 3 {
		t.Fatal("cleanup not persisted")
	}
	after, _ := os.ReadFile(native)
	if string(after) != string(original) {
		t.Fatal("cleanup changed native credentials")
	}
}

func TestDefaultStoreIsUnderDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if got := DefaultStore().Root; got != "/data/dyna/accounts" {
		t.Fatalf("DefaultStore().Root = %q", got)
	}
}
