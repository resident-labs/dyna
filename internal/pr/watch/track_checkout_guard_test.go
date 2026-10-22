package watch

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/store"
)

func TestTrackingCheckoutProbeDoesNotHoldPublicationGuard(t *testing.T) {
	testTrackingCheckoutGuard(t, "")
}

func TestTrackingRefusesChangedLocalCheckoutIdentity(t *testing.T) {
	for _, field := range []string{"author", "room", "branch", "dir", "checkout", "removed", "added"} {
		t.Run(field, func(t *testing.T) { testTrackingCheckoutGuard(t, field) })
	}
}

func testTrackingCheckoutGuard(t *testing.T, change string) {
	t.Helper()
	e := newEnv(t)
	local := Record{Key: "local-change", ChangeID: "stable-change", Kind: "local", Branch: "feature", RepoPath: "another-checkout", Dir: e.author.Dir, RoomID: e.author.RoomID, Author: e.author.ID, State: "open", Watching: true, Head: e.f.Pull.Head.SHA}
	if err := e.s.Store.update(func(st *State) error { st.Records[local.Key] = &local; return nil }); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.Ref = local.Branch
	e.write()
	original := resolveCheckoutRepo
	t.Cleanup(func() { resolveCheckoutRepo = original })
	var probeErr error
	var expected State
	probes := 0
	resolveCheckoutRepo = func(_ context.Context, dir string) (repoid.Repo, error) {
		probes++
		unlock, owned, err := store.TryLock(state.LockPath(e.s.Store.Root))
		if err != nil || !owned {
			probeErr = fmt.Errorf("checkout probe retained the publication guard: owned=%v, error=%v", owned, err)
			return repoid.Repo{}, probeErr
		}
		unlock()
		// This write completes before the checkout probe returns. It models
		// a change that arrives while Git inspects the repository.
		probeErr = e.s.Store.update(func(st *State) error {
			r := st.Records[local.Key]
			switch change {
			case "author":
				r.Author = "another-author"
			case "room":
				r.RoomID = "another-room"
			case "branch":
				r.Branch = "another-branch"
			case "dir":
				r.Dir = "another-directory"
			case "checkout":
				r.RepoPath = "changed-checkout"
			case "removed":
				delete(st.Records, local.Key)
			case "added":
				other := *r
				other.Key, other.ChangeID = "another-local-change", "another-change"
				st.Records[other.Key] = &other
			default:
				r.Notes.Testing = "independent writer committed during the checkout probe"
			}
			return nil
		})
		if probeErr == nil {
			expected, probeErr = e.s.Store.Read()
		}
		id, err := repoid.RemoteIdentity("", "https://github.com/owner/repo")
		return repoid.Repo{ID: id, Root: dir, Main: dir}, err
	}
	ref := Ref{Host: "github.com", Repo: "owner/repo", Number: 17}
	got, created, err := e.s.Track(t.Context(), authority.Actor{Role: authority.Owner}, ref, e.author, "")
	if probeErr != nil || probes != 1 {
		t.Fatalf("checkout boundary: probes=%d, error=%v", probes, probeErr)
	}
	if change != "" {
		if err == nil || !strings.Contains(err.Error(), "local change changed while checking its checkout") {
			t.Fatalf("tracking accepted a changed %s: %v", change, err)
		}
		after, readErr := e.s.Store.Read()
		if readErr != nil || !reflect.DeepEqual(after.Records, expected.Records) {
			t.Fatalf("refused tracking changed records after %s: %v", change, readErr)
		}
		return
	}
	if err != nil || !created || got.ChangeID != local.ChangeID || got.Kind != "github" || got.Notes.Testing == "" {
		t.Fatalf("attachment lost the live local change: %+v, %v", got, err)
	}
	st, err := e.s.Store.Read()
	if err != nil || len(st.Records) != 1 || st.Records[ref.Key()] == nil {
		t.Fatalf("attachment left duplicate records: %+v, %v", st.Records, err)
	}
}
