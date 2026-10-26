package watch

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
)

// D-107: closing a child session removed the worktree its PRs were tracked
// from, and every review launch then failed with "working directory ... is not
// a directory" until that folder came back.
func TestReviewerDirSkipsRemovedWorktree(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), nil)
	r := e.track()
	roomRec, err := e.s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	if err := e.s.Store.update(func(st *State) error {
		rec := st.Records[r.Key]
		rec.Dir, rec.LookupDir, rec.RepoPath = gone, gone, ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := e.s.Store.Get(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := e.s.reviewerDir(rec)
	if err != nil {
		t.Fatalf("reviewer directory: %v", err)
	}
	if dir != roomRec.Repo {
		t.Fatalf("reviewer dir = %q, want the room repository %q", dir, roomRec.Repo)
	}
	if _, err := e.s.reviewerDir(Record{RoomID: r.RoomID}); err != nil {
		t.Fatalf("record with no directory: %v", err)
	}
}
