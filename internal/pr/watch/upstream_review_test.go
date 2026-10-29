package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// A tracked pull request into the room's upstream is read only. The reviewer
// worker takes its work from the review queue, so the queue must skip it.
func TestUpstreamPRIsNotQueuedForReview(t *testing.T) {
	s, r, _, _, _, _ := trialStartEnv(t, []string{"relay"})
	if err := s.Notify.Rooms.UpdatePreferences(r.RoomID, func(rec *room.Record) error {
		rec.Upstream = "github.com/horizon-rust/horizon"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	head := "0123456789abcdef0123456789abcdef01234567"
	upstream := &Record{Kind: "github", Key: "upstream", RoomID: r.RoomID, Host: "github.com", Repo: "horizon-rust/horizon", HeadRepo: "levzzz/horizon", Number: 3964,
		Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature",
		Watching: true, State: state.PROpen, CI: "absent", Created: time.Unix(1000, 0)}
	st := State{Records: map[string]*Record{upstream.Key: upstream}}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if queue := facts.Queue(r.RoomID); len(queue) != 0 {
		t.Fatalf("review queue holds %d upstream pull requests, want none", len(queue))
	}
}
