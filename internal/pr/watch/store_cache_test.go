package watch

import (
	"testing"

	"dyna-agent/internal/pr/state"
)

func TestNoopUpdateKeepsRevision(t *testing.T) {
	s := &Store{Root: state.Root(t.TempDir())}
	if err := s.update(func(st *State) error {
		st.Records["pr"] = &Record{Key: "pr", State: "open", Watching: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := state.Revision(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if after, err := state.Revision(s.Root); err != nil || after != before {
		t.Fatalf("no-op update moved the revision %s -> %s and woke store scanners (%v)", before, after, err)
	}
}
