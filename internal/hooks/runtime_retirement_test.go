package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestRuntimeIgnoresStaleFileAfterCutover(t *testing.T) {
	s := Service{DataDir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.DataDir) })
	path := filepath.Join(s.dir("repo"), "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, state{Main: "legacy"}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "legacy" {
			t.Fatalf("imported state = %+v", st)
		}
		st.Main = "canonical"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, state{Main: "stale"}, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "canonical" {
			t.Fatalf("stale source replaced canonical state: %+v", st)
		}
		st.Notices["kept"] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(source) {
		t.Fatalf("source changed: %s, %v", after, err)
	}
	if err := database.Close(s.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(s.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := s.runtime("repo", func(st *state) error {
		if st.Main != "canonical" || !st.Notices["kept"] {
			t.Fatalf("reopened state = %+v", st)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
