package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/repoid"
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

// A failed upstream pull request notifies its author only when the head is one of
// the room's checkouts. A fork head is read-only here, so its failure stays quiet
// while the failure count still updates.
func TestUpstreamPRFailureNoticeFollowsHeadRepo(t *testing.T) {
	const extraDir = "/extra/checkout"
	checkouts := map[string]string{extraDir: "github.com/mine/extra"}
	original := resolveCheckoutRepo
	t.Cleanup(func() { resolveCheckoutRepo = original })
	resolveCheckoutRepo = func(_ context.Context, dir string) (repoid.Repo, error) {
		id, ok := checkouts[dir]
		if !ok {
			id = "github.com/mine/repo"
		}
		return repoid.Repo{ID: id, Root: dir, Main: dir}, nil
	}
	for _, tc := range []struct {
		name, head string
		notified   bool
	}{
		{"read-only fork", "someone/repo", false},
		{"room's own fork", "mine/repo", true},
		{"room's extra repository fork", "mine/extra", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			rooms := room.DefaultStore()
			e.s.Notify = notify.New(rooms, subagent.NewManager())
			if err := rooms.UpdatePreferences(e.author.RoomID, func(rec *room.Record) error {
				rec.Upstream = "github.com/owner/repo"
				rec.ExtraRepos = []string{extraDir}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			e.f.Pull.Head.Repo.FullName = tc.head
			e.track()
			e.tick()
			e.tick()
			if got := e.record().Failures; got != 1 {
				t.Fatalf("failures = %d, want 1", got)
			}
			if !tc.notified {
				if len(e.sent) != 0 || len(e.record().Notices) != 0 {
					t.Fatalf("read-only fork woke its author: %+v", e.sent)
				}
				return
			}
			if len(e.sent) != 1 || e.sent[0].Target != e.author.ID || !strings.Contains(e.sent[0].Text, "CI failed (1/3)") {
				t.Fatalf("own fork did not send its failure notice: %+v", e.sent)
			}
		})
	}
}
