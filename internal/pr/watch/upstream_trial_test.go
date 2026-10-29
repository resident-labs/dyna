package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/room"
)

// A trial made before its room recorded the upstream still holds the upstream
// PR. Advancing it must drop the trial before anything publishes.
func TestUpstreamMemberDropsItsTrialBeforePublishing(t *testing.T) {
	ctx := context.Background()
	s, r, _, repoKey, _, _ := trialStartEnv(t, []string{"relay"})
	if err := s.Notify.Rooms.UpdatePreferences(r.RoomID, func(rec *room.Record) error {
		rec.Upstream = "github.com/owner/repo"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.advanceMergeTrial(ctx, repoKey, trialStartHead(t, s, repoKey)); err != nil {
		t.Fatal(err)
	}
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if batch := st.MergeTrials[repoKey]; batch != nil {
		t.Fatalf("trial with an upstream pull request is still active: phase %q", batch.Phase)
	}
}
