package watch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/links"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func transferTarget(e *testEnv) subagent.Info {
	target := e.author
	target.ID = "ses_20261017-000004-dddddddd"
	target.Name = "successor"
	return target
}

func TestTransferPRAuthorPreservesMarkersAndMovesPendingNotices(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	target := transferTarget(e)
	if err := e.s.Store.update(func(st *State) error {
		notice(st.Records[r.Key], e.author.ID, "CI failure", "before-transfer")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Transfer(context.Background(), r.Key, "author", target, ""); err != nil {
		t.Fatal(err)
	}
	current := e.record()
	if current.Author != target.ID || len(current.FormerAuthors) != 1 || current.FormerAuthors[0] != e.author.ID || current.Dir != r.Dir {
		t.Fatalf("author transfer: %+v", current)
	}
	for _, n := range e.sent {
		if n.Target != target.ID {
			t.Fatalf("notice still targets old author: %+v", n)
		}
	}
	links, err := links.ForSession(e.s.Store.Root, e.author.ID, r.RoomID)
	if err != nil || len(links) != 0 {
		t.Fatalf("old author guard: %+v %v", links, err)
	}
	if err := e.s.Transfer(context.Background(), r.Key, "author", target, ""); err != nil {
		t.Fatal(err)
	}
	if got := e.record(); len(got.FormerAuthors) != 1 {
		t.Fatalf("retry changed history: %+v", got)
	}
}

func TestTransferRejectsArbitraryReviewer(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	if err := e.s.Transfer(context.Background(), r.Key, "reviewer", transferTarget(e), ""); err == nil || !strings.Contains(err.Error(), "managed reviewer") {
		t.Fatalf("reviewer override: %v", err)
	}
}

// pr/view checks who may transfer (TestTransferAndDraftActForTheirCaller).
func TestTransferRejectsRoomClosedTargetAndRoleCollision(t *testing.T) {
	for _, scenario := range []string{"room", "closed", "review-self", "author-reviewer", "role"} {
		t.Run(scenario, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			target := transferTarget(e)
			role := "author"
			switch scenario {
			case "room":
				target.RoomID = "another-room"
			case "closed":
				meta := target.Meta
				meta.Closed = time.Now()
				recordtest.Write(e.t, filepath.Join(filepath.Dir(e.s.Store.Root), "sessions"), target.ID, meta)
			case "review-self":
				role = "reviewer"
				target = e.author
			case "author-reviewer":
				_ = e.s.Store.update(func(st *State) error { st.Records[r.Key].Reviewer = target.ID; return nil })
			case "role":
				role = "lead"
			}
			if err := e.s.Transfer(context.Background(), r.Key, role, target, ""); err == nil {
				t.Fatalf("accepted %s", scenario)
			}
			if got := e.record(); got.Author != e.author.ID {
				t.Fatalf("failed transfer changed author: %+v", got)
			}
		})
	}
}
