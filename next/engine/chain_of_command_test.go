package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func countRows(t *testing.T, s *Service, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.store.Read().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A session's mention outside its parent and its own children is refused with one line, and nothing of the post is kept.
func TestMentionOutsideChainIsRefused(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	peer, problem := s.SessionStart(ctx, asSession(worker), contract.SessionStartRequest{Room: room, Name: "peer", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Name: "other", Profile: "test", Goal: "goal"}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "elsewhere", RootProfile: "test"}); problem != nil {
		t.Fatal(problem)
	}
	var rootName string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT name FROM sessions WHERE id=?", root).Scan(&rootName); err != nil {
		t.Fatal(err)
	}
	posts := func() int { return countRows(t, s, ctx, "SELECT COUNT(*) FROM events WHERE type='post.created'") }
	deliveries := func() int { return countRows(t, s, ctx, "SELECT COUNT(*) FROM deliveries") }
	beforePosts, beforeDeliveries := posts(), deliveries()
	for _, c := range []struct {
		from    contract.SessionID
		mention string
		want    string
	}{
		{kid, "@peer", "you can message your parent (worker) and your children; your parent passes it on."},
		{kid, string(peer.Session), "you can message your parent (worker) and your children; your parent passes it on."},
		{kid, "@siblings", "you can message your parent (worker) and your children; your parent passes it on."},
		{kid, "@root", "you can message your parent (worker) and your children; your parent passes it on."},
		{kid, "@" + rootName, "you can message your parent (worker) and your children; your parent passes it on."},
		{kid, "@other", "you can message your parent (worker) and your children; your parent passes it on."},
		{kid, "@elsewhere:root", "you can message your parent (worker) and your children; your parent passes it on."},
		{worker, "@siblings", "you can message your parent (" + rootName + ") and your children; your parent passes it on."},
		{root, "@kid", "you can message your children; a post that mentions no session goes to the owner."},
	} {
		// The parent beside the refused mention is not reached either.
		_, problem := s.Post(ctx, asSession(c.from), contract.PostRequest{Room: room, Text: "hello", Mentions: []string{"@parent", c.mention}})
		if problem == nil || problem.Code != contract.ErrForbidden || problem.Message != c.want {
			t.Errorf("%s mentions %s: %+v", c.from, c.mention, problem)
		}
	}
	if posts() != beforePosts || deliveries() != beforeDeliveries {
		t.Fatalf("refused posts left %d posts and %d deliveries", posts()-beforePosts, deliveries()-beforeDeliveries)
	}
}

// A session reaches its parent, as parent or by name or ID, and its own children, as children or by name or ID.
func TestParentAndChildrenAreReached(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	for _, c := range []struct {
		from    contract.SessionID
		mention string
		to      contract.SessionID
	}{
		{kid, "@parent", worker},
		{kid, "@worker", worker},
		{kid, string(worker), worker},
		{worker, "@children", kid},
		{worker, "@kid", kid},
		{worker, string(kid), kid},
		{worker, "@root", root}, // root is worker's parent
		{root, "@worker", worker},
	} {
		before := pendingTo(t, s, ctx, c.to)
		if _, problem := s.Post(ctx, asSession(c.from), contract.PostRequest{Room: room, Text: "hello", Mentions: []string{c.mention}}); problem != nil {
			t.Fatalf("%s mentions %s: %+v", c.from, c.mention, problem)
		}
		if n := pendingTo(t, s, ctx, c.to); n != before+1 {
			t.Fatalf("%s mentions %s: %d new inputs", c.from, c.mention, n-before)
		}
	}
}

// A post to a parent that can't receive goes to the parent's nearest ancestor that can, and its frame says whom it was for.
func TestPostGoesUpPastParentThatCantReceive(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*Service, context.Context, contract.SessionID)
		want string
	}{
		{"failed", func(s *Service, ctx context.Context, id contract.SessionID) {
			setBox(t, s, ctx, id, contract.BoxFailed, "host removed")
		}, "box failed"},
		{"lost", func(s *Service, ctx context.Context, id contract.SessionID) {
			setBox(t, s, ctx, id, contract.BoxLost, "")
		}, "box lost"},
		{"unplaced", func(s *Service, ctx context.Context, id contract.SessionID) {
			setBox(t, s, ctx, id, contract.BoxPending, "no-ready-host")
		}, "box can't be placed"},
		{"closing", func(s *Service, ctx context.Context, id contract.SessionID) {
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closing' WHERE id=?", id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}, "closing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, room, root, worker, kid, ctx := receiveTree(t)
			c.set(s, ctx, worker)
			workerBefore, rootBefore := pendingTo(t, s, ctx, worker), pendingTo(t, s, ctx, root)
			sent, problem := s.Post(ctx, asSession(kid), contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@parent"}})
			if problem != nil {
				t.Fatal(problem)
			}
			if pendingTo(t, s, ctx, worker) != workerBefore || pendingTo(t, s, ctx, root) != rootBefore+1 {
				t.Fatal("the post did not go up to root")
			}
			var meantFor contract.SessionID
			var why string
			if err := s.store.Read().QueryRowContext(ctx, "SELECT meant_for,meant_why FROM deliveries WHERE to_id=? AND seq=?", root, sent.Seq).Scan(&meantFor, &why); err != nil {
				t.Fatal(err)
			}
			if meantFor != worker || why != c.want {
				t.Fatalf("meant for %s because %q", meantFor, why)
			}
			event, err := s.store.EventInRoom(ctx, room, sent.Event)
			if err != nil {
				t.Fatal(err)
			}
			frame, _, err := s.inputFrame(ctx, root, event, contract.Delivery{Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyPost}, MeantFor: meantFor, Why: why})
			if line := "[Dyna] From \"kid\", in the room feed. For worker, which can't receive (" + c.want + ")."; err != nil || !strings.HasPrefix(frame, line) {
				t.Fatalf("frame %q, want it to start %q: %v", frame, line, err)
			}
		})
	}
}

// Root's box reads the frame line on the input that came up to it.
func TestFrameOfAPostThatWentUp(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	setBox(t, s, ctx, worker, contract.BoxFailed, "host removed")
	if _, problem := s.Post(ctx, asSession(kid), contract.PostRequest{Room: room, Text: "result", Mentions: []string{"@parent"}}); problem != nil {
		t.Fatal(problem)
	}
	channel, problem := testChannel(t, s, ctx, asSession(root))
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-channel).Body.(contract.TurnStart)
	for _, input := range start.Inputs {
		if inputBody(input.Text) == "result" {
			if !strings.HasPrefix(input.Text, "[Dyna] From \"kid\", in the room feed. For worker, which can't receive (box failed). No reply is delivered.") {
				t.Fatalf("frame %q", input.Text)
			}
			return
		}
	}
	t.Fatalf("root's turn has no post from kid: %+v", start.Inputs)
}

// A child's final reply to a parent that can't receive goes up the same way.
func TestFinalReplyGoesUpPastParentThatCantReceive(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	if _, problem := s.Post(ctx, asSession(worker), contract.PostRequest{Room: room, Text: "task", Mentions: []string{"@kid"}}); problem != nil {
		t.Fatal(problem)
	}
	channel, problem := testChannel(t, s, ctx, asSession(kid))
	if problem != nil {
		t.Fatal(problem)
	}
	var start contract.TurnStart
	select {
	case message := <-channel:
		start = message.Body.(contract.TurnStart)
	case <-time.After(10 * time.Second):
		t.Fatal("no turn reached kid")
	}
	setBox(t, s, ctx, worker, contract.BoxFailed, "host removed")
	workerBefore := pendingTo(t, s, ctx, worker)
	if _, problem := s.TurnOutput(ctx, asSession(kid), contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Final: "done"}); problem != nil {
		t.Fatal(problem)
	}
	var to, meantFor contract.SessionID
	var why string
	err := s.store.Read().QueryRowContext(ctx, "SELECT d.to_id,d.meant_for,d.meant_why FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='turn.ended' AND json_extract(e.body,'$.turn')=?", start.Turn).Scan(&to, &meantFor, &why)
	if err != nil || to != root || meantFor != worker || why != "box failed" {
		t.Fatalf("final reply to %s, meant for %s because %q: %v", to, meantFor, why, err)
	}
	if pendingTo(t, s, ctx, worker) != workerBefore {
		t.Fatal("the failed parent got the reply")
	}
}

// A session's subscription may wake only itself or its own descendants. The owner's may wake anyone.
func TestSubscriptionWakesStayInTheChain(t *testing.T) {
	s, room, root, worker, kid, ctx := receiveTree(t)
	add := func(caller contract.Principal, session contract.SessionID, wake contract.SessionID) *contract.Error {
		_, problem := s.SubscriptionAdd(ctx, caller, contract.SubscriptionAddRequest{Scope: contract.ScopeSession, Room: room, Session: string(session), Every: "1h", Wake: string(wake)})
		return problem
	}
	for _, wake := range []contract.SessionID{worker, root} {
		if problem := add(asSession(kid), kid, wake); problem == nil || problem.Code != contract.ErrForbidden || problem.Message != "a session can wake only itself and its descendants" {
			t.Fatalf("kid wakes %s: %+v", wake, problem)
		}
	}
	if problem := add(asSession(worker), worker, root); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("worker wakes root: %+v", problem)
	}
	for _, c := range []struct {
		caller  contract.Principal
		session contract.SessionID
		wake    contract.SessionID
	}{
		{asSession(kid), kid, kid},
		{asSession(worker), kid, kid},
		{asSession(root), kid, kid},
		{contract.Principal{Kind: contract.PrincipalOwner}, kid, root},
	} {
		if problem := add(c.caller, c.session, c.wake); problem != nil {
			t.Fatalf("%+v wakes %s: %+v", c.caller, c.wake, problem)
		}
	}
}

// A crew reports only to its parent, the session that started it, by parent, name or ID.
func TestCrewPostsOnlyToItsStarter(t *testing.T) {
	w := setupWorkflow(t)
	var rootName string
	if err := w.store.Read().QueryRowContext(w.ctx, "SELECT name FROM sessions WHERE id=?", w.root).Scan(&rootName); err != nil {
		t.Fatal(err)
	}
	_, crew := w.start(asSession(w.root), workflowScript)
	if _, problem := w.SessionStart(w.ctx, w.owner, contract.SessionStartRequest{Room: w.room, Name: "peer", Profile: "fast", Goal: "goal"}); problem != nil {
		t.Fatal(problem)
	}
	for _, mention := range []string{"parent", rootName, string(w.root)} {
		if _, problem := w.Post(w.ctx, crew, contract.PostRequest{Room: w.room, Text: "report", Mentions: []string{mention}}); problem != nil {
			t.Fatalf("crew mentions %s: %+v", mention, problem)
		}
	}
	want := "a workflow can message only its parent (" + rootName + ")."
	for _, mention := range []string{"peer", "siblings"} {
		_, problem := w.Post(w.ctx, crew, contract.PostRequest{Room: w.room, Text: "report", Mentions: []string{mention}})
		if problem == nil || problem.Code != contract.ErrForbidden || problem.Message != want {
			t.Fatalf("crew mentions %s: %+v", mention, problem)
		}
	}
}
