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
	info, err := os.Stat(s.credentialPath(original.ID))
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
	beforeState, _ := s.Read()
	before, _ := json.Marshal(beforeState)
	err := s.Update(func(st *State) error { st.Entries = nil; return errors.New("activation failed") })
	afterState, _ := s.Read()
	after, _ := json.Marshal(afterState)
	if err == nil || string(before) != string(after) {
		t.Fatal("failed transaction changed saved state")
	}

}

func TestSameIdentityInDifferentIntegrationsStaysSeparate(t *testing.T) {
	st := State{}
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

func TestDefaultStoreIsUnderDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	if got := DefaultStore().Root; got != "/data/dyna/accounts" {
		t.Fatalf("DefaultStore().Root = %q", got)
	}
}
