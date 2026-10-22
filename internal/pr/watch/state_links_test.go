package watch

import (
	"reflect"
	"testing"

	prlinks "dyna-agent/internal/pr/links"
	"dyna-agent/internal/pr/state"
)

func TestStoredPRSnapshotFeedsSessionLinks(t *testing.T) {
	store := &Store{Root: state.Root(t.TempDir())}
	record := Record{
		Key: "github.com/owner/repo#17", Number: 17, URL: "https://github.com/owner/repo/pull/17",
		RoomID: "room", Author: "author", ReviewerOverride: "reviewer", Reviewer: "configured-reviewer",
		Dir: "/worktree", LookupDir: "/checkout", Head: "head", State: "open", Watching: true,
	}
	if err := store.update(func(st *State) error {
		st.Records[record.Key] = &record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for session, roles := range map[string][]string{
		"author": {"author"}, "reviewer": {"reviewer"}, "configured-reviewer": {"reviewer"},
	} {
		links, err := prlinks.ForSession(store.Root, session, record.RoomID)
		want := []prlinks.Link{{Key: record.Key, Number: record.Number, URL: record.URL, Dir: record.Dir,
			LookupDir: record.LookupDir, Head: record.Head, State: record.State, Watching: true, Roles: roles}}
		if err != nil || !reflect.DeepEqual(links, want) {
			t.Fatalf("%s links: %+v, %v; want %+v", session, links, err, want)
		}
	}
}
