package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/state"
)

// A room that no enabled CI machine serves never gets a run, so the trial fails
// with the cause and the fix instead of waiting for local CI for ever.
func TestTrialFailsWhenNoCIMachineServesTheRoom(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if _, err := (machines.Store{DataDir: dataDir}).Save(ctx, machines.Machine{Name: "worker", Roles: []machines.Role{machines.RoleCI}, SSH: "ari@worker", CISlots: 1, Enabled: true, Rooms: []string{"other-room"}}); err != nil {
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
