package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

func TestTurnEndedByCloseIsNotAFailedTurn(t *testing.T) {
	r := newGoalRig(t)
	turn := r.begin(r.child)
	if _, e := r.s.SessionClose(r.ctx, asSession(r.lead), contract.SessionCloseRequest{Session: r.child}); e != nil {
		t.Fatal(e)
	}
	// Close stops the agent program, and the runner reports that as a failed harness.
	r.end(r.child, turn, contract.TurnFailed, contract.CauseHarnessFailed, "")
	var outcome contract.TurnOutcome
	for _, ended := range eventBodies[contract.TurnEnded](r, "turn.ended", r.lead) {
		if ended.Session == r.child {
			t.Fatalf("the parent got the close's turn end: %+v", ended)
		}
	}
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT outcome FROM turns WHERE id=?", turn).Scan(&outcome); err != nil || outcome != contract.TurnStopped {
		t.Fatalf("outcome %q: %v", outcome, err)
	}
	r.pass()
	if got := r.failures(r.lead); len(got) != 0 {
		t.Fatalf("failure notices %+v", got)
	}
}

func TestGoalHandedToRootIsListedAndEnded(t *testing.T) {
	r := newGoalRig(t)
	started, e := r.s.SessionStart(r.ctx, ownerCaller, contract.SessionStartRequest{Room: r.room, Name: "solo", Profile: "test", Goal: "port it", DoneWhen: "merged"})
	if e != nil {
		t.Fatal(e)
	}
	goals, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room, Session: started.Session})
	if e != nil || len(goals.Goals) != 1 {
		t.Fatalf("goals %+v: %v", goals, e)
	}
	goal := goals.Goals[0].ID
	if _, e := r.s.GoalItem(r.ctx, ownerCaller, contract.GoalItemRequest{Room: r.room, Goal: goal, Text: "unit"}); e != nil {
		t.Fatal(e)
	}
	closed, e := r.s.SessionClose(r.ctx, ownerCaller, contract.SessionCloseRequest{Session: started.Session})
	if e != nil {
		t.Fatal(e)
	}
	if closed.To != r.root || len(closed.Unheld) != 1 || closed.Unheld[0] != goal || len(closed.Handed) != 0 {
		t.Fatalf("close result %+v", closed)
	}
	finished, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: started.Session})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.st.CloseSession(r.ctx, started.Session, finished); err != nil {
		t.Fatal(err)
	}
	listed, e := r.s.GoalList(r.ctx, asSession(r.root), contract.GoalListRequest{Session: r.root})
	if e != nil || len(listed.Goals) != 1 || listed.Goals[0].ID != goal {
		t.Fatalf("root's list %+v: %v", listed, e)
	}
	if _, e := r.s.GoalDone(r.ctx, asSession(r.root), contract.GoalDoneRequest{Goal: goal, Summary: "not needed"}); e != nil {
		t.Fatalf("goal done: %v", e)
	}
	ended, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room, All: true, Session: r.root})
	if e != nil || len(ended.Goals) != 1 || ended.Goals[0].State != contract.GoalDone || ended.Goals[0].Items[0].State != contract.GoalItemDropped {
		t.Fatalf("ended %+v: %v", ended, e)
	}
}

func TestSessionListPagesForASession(t *testing.T) {
	r := newGoalRig(t)
	for i := range 30 {
		if _, e := r.s.SessionStart(r.ctx, ownerCaller, contract.SessionStartRequest{Room: r.room, Name: fmt.Sprintf("w%02d", i), Profile: "test"}); e != nil {
			t.Fatal(e)
		}
	}
	root := asSession(r.root)
	page, e := r.s.SessionList(r.ctx, root, contract.SessionListRequest{Room: r.room})
	if e != nil || len(page.Sessions) != contract.SessionListPage || page.Next != page.Sessions[len(page.Sessions)-1].ID {
		t.Fatalf("first page %d next %q: %v", len(page.Sessions), page.Next, e)
	}
	rest, e := r.s.SessionList(r.ctx, root, contract.SessionListRequest{Room: r.room, After: page.Next})
	if e != nil || rest.Next != "" || len(page.Sessions)+len(rest.Sessions) != 33 {
		t.Fatalf("rest %d next %q: %v", len(rest.Sessions), rest.Next, e)
	}
	all, e := r.s.SessionList(r.ctx, ownerCaller, contract.SessionListRequest{Room: r.room})
	if e != nil || len(all.Sessions) != 33 || all.Next != "" {
		t.Fatalf("owner list %d: %v", len(all.Sessions), e)
	}
	children, e := r.s.SessionList(r.ctx, root, contract.SessionListRequest{Room: r.room, Parent: r.lead})
	if e != nil || len(children.Sessions) != 1 || children.Sessions[0].ID != r.child {
		t.Fatalf("children %+v: %v", children.Sessions, e)
	}
}

func TestTurnWithNothingOwedIsNotSilent(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.notice(r.child) // an FYI that owes no reply
	r.turn(r.child, "noted")
	r.advance(time.Second)
	r.pass()
	r.wantCheckins(r.lead, 0)
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
}

func TestWorkingTurnIsNotStalled(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.notice(r.child)
	turn := r.begin(r.child)
	for range 9 {
		r.advance(20 * time.Minute)
		if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: turn, Label: "git push"}); e != nil {
			t.Fatal(e)
		}
		r.pass()
	}
	r.wantCheckins(r.lead, 0)
	r.advance(room.CheckinStuck + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
}

func TestActivityKindReachesTheSessionList(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.notice(r.child)
	turn := r.begin(r.child)
	kindOf := func() contract.ActivityKind {
		page, e := r.s.SessionList(r.ctx, ownerCaller, contract.SessionListRequest{Room: r.room})
		if e != nil {
			t.Fatal(e)
		}
		for _, session := range page.Sessions {
			if session.ID == r.child {
				return session.ActivityKind
			}
		}
		t.Fatal("no child")
		return ""
	}
	for _, tc := range []struct{ kind, want contract.ActivityKind }{
		{contract.ActivityCompaction, contract.ActivityCompaction},
		{contract.ActivityModel, contract.ActivityModel},
		{"", contract.ActivityTool}, // a box agent that predates kinds
	} {
		if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: turn, Kind: tc.kind, Label: "x"}); e != nil {
			t.Fatal(e)
		}
		if got := kindOf(); got != tc.want {
			t.Fatalf("kind %q listed as %q, want %q", tc.kind, got, tc.want)
		}
	}
	if _, e := r.s.TurnActivity(r.ctx, asSession(r.child), contract.TurnActivityRequest{Turn: turn, Kind: "dancing", Label: "x"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("unknown kind: %+v", e)
	}
}
