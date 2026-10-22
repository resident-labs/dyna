package library

import (
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestMutationsUseSharedWriter(t *testing.T) {
	s := Store{DataDir: t.TempDir(), Actor: authority.Actor{Role: authority.Owner}}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(s.DataDir) })
	writer := state.For(db)
	writer.SetPublish(func(event hub.Event) { t.Errorf("unexpected watched topic: %+v", event) })
	before := writer.Health().Writes
	if _, err := s.Add("repo", entry("feature", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("repo", "feature", func(e *Entry) error { e.Summary = "Updated."; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retire("repo", "feature", ""); err != nil {
		t.Fatal(err)
	}
	if got := writer.Health().Writes - before; got != 3 {
		t.Fatalf("shared writer counted %d writes; want 3", got)
	}
	got, err := s.Get("repo", "feature")
	if err != nil || !got.Retired || got.Summary != "Updated." {
		t.Fatalf("entry = %+v, error = %v", got, err)
	}
}
