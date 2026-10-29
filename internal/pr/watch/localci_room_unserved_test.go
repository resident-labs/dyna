package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/state"
)

// With no enabled CI machine at all, the gate returns before it judges a saved
// result, so it must report the cause itself.
func TestGateFailsTrialWhenFleetHasNoEnabledCIMachine(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repoDir, base, head, _ := trialRepoWithDefinition(t)
	registry := localci.Store{DataDir: dataDir}
	if err := registry.SetMode(ctx, "room-test", localci.ModeLocal); err != nil {
		t.Fatal(err)
	}
	// The engine host row carries no ci label, so the fleet has no CI machine.
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Base: base, Branch: "dyna/merge-trial/1", RepoPath: repoDir, RoomID: "room-test", State: "open", Watching: true}
	s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry}
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Base: base, Head: head, Branch: r.Branch, Phase: "running"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap := Snapshot{}
	snap.Pull.Head.SHA, snap.Pull.Head.Repo.FullName = head, r.Repo
	got, err := s.withRegisteredCI(ctx, r, snap, ChangePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	want := localci.RoomNotServedError{RoomID: "room-test"}.Error()
	if got.localCI == nil || got.localCI.Status != VerdictFailed || got.localCI.Reason != want {
		t.Fatalf("local CI = %+v, want failed with %q", got.localCI, want)
	}
}

// A room that no enabled CI machine serves never gets a run, so the trial fails
// with the cause and the fix instead of waiting for local CI for ever.
func TestTrialFailsWhenNoCIMachineServesTheRoom(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if _, err := (machines.Store{DataDir: dataDir}).Save(ctx, machines.Machine{Name: "worker", SSH: "ari@worker", Enabled: true, Labels: []string{machines.LabelCI}, Rooms: []string{"other-room"}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: localci.Store{DataDir: dataDir}}
	r := Record{Kind: "github", Key: "pr", Repo: "owner/repo", RoomID: "room-test"}
	got := s.registeredLocalObservation(r, Snapshot{}, ChangePolicy{}, localCIPlan{}, localci.Result{}, true, false)
	want := localci.RoomNotServedError{RoomID: "room-test"}
	if got.Status != VerdictFailed || got.Reason != want.Error() {
		t.Fatalf("status = %s, reason = %q, want failed with %q", got.Status, got.Reason, want)
	}
}
