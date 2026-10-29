package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// A pull request into the room's upstream runs the upstream's CI. The room's
// CI budget and JUnit rules must not warn or fail its author.
func TestUpstreamPRSendsNoCIBudgetNotes(t *testing.T) {
	e := newEnv(t)
	start := e.clock
	rooms := room.DefaultStore()
	e.s.Notify = notify.New(rooms, subagent.NewManager())
	if err := rooms.UpdatePreferences(e.author.RoomID, func(rec *room.Record) error {
		rec.Upstream = "github.com/owner/repo"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	budgetFixture(e, start, "pull_request")
	e.track()
	e.tick()
	e.clock = e.clock.Add(30 * time.Second)
	e.tick()
	e.tick()
	if len(e.sent) != 0 || len(e.record().Notices) != 0 {
		t.Fatalf("upstream pull request woke its author with CI budget notes: %+v", e.sent)
	}
}
