package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
)

// D-131: a merge retry ran git in the worktree of a closed session. Git then
// failed with "fork/exec /usr/bin/git: no such file or directory", because Go
// reports a missing command directory that way and names git, not the folder.
func TestMergeRetryRunsGitInALiveCheckout(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), nil)
	r := e.track()
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	if err := e.s.Store.update(func(st *State) error {
		rec := st.Records[r.Key]
		rec.Dir, rec.LookupDir, rec.RepoPath = gone, gone, gone
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec, err := e.s.Store.Get(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	job := registeredCIJob{record: rec, head: rec.Head, branch: rec.Branch}
	_, err = e.s.savedTrialCI(context.Background(), job)
	if err != nil && strings.Contains(err.Error(), "fork/exec") {
		t.Fatalf("git ran in the removed folder %s: %v", gone, err)
	}
	if err != nil && strings.Contains(err.Error(), "not a directory any more") {
		t.Fatalf("the room repository was not used: %v", err)
	}
	// Without any live checkout the error names the folder, not git.
	_, err = e.s.checkoutDir(context.Background(), Record{RoomID: "room_missing", Key: "github.com/owner/repo#404", Dir: gone})
	if err == nil || !strings.Contains(err.Error(), gone) || strings.Contains(err.Error(), "fork/exec") {
		t.Fatalf("error for a removed checkout = %v, want it to name %s", err, gone)
	}
	// LookupDir and Dir are session directories, not proven checkouts, so a
	// GitHub record keeps looking when one of them belongs to another repository.
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "unrelated"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := e.s.checkoutDir(context.Background(), Record{RoomID: r.RoomID, Host: rec.Host, Repo: rec.Repo, LookupDir: filepath.Join(other, "unrelated")})
	if err != nil {
		t.Fatalf("unrelated directory: %v", err)
	}
	roomRec, err := e.s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if dir != roomRec.Repo {
		t.Fatalf("checkout dir = %q, want the room repository %q", dir, roomRec.Repo)
	}
}
