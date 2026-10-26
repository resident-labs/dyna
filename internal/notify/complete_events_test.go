package notify

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// A detached shell posts a typed result: its label, state, exit code, duration
// and output lines are facts the row reads, and the agent text keeps the tail
// and the pointer to the full output.
func TestShellCompletionPostsTypedResult(t *testing.T) {
	f := newFixture(t)
	id := "sh_20261023-120000-0000000a"
	res, err := f.svc.CompleteShell(f.ctx, ShellExit{ID: id, RoomID: f.a.ID, Parent: f.lead.ID, Command: "go", Description: "run the room tests",
		Code: 2, Duration: 1500 * time.Millisecond, Tail: "first line\nFAIL dyna-agent/internal/room\n"})
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := res.Message.Shell()
	if !ok {
		t.Fatalf("stored post is not a shell result: %+v", res.Message)
	}
	if ev.State != room.ShellFailed || ev.ExitCode != 2 || ev.Label != "go · run the room tests" || ev.DurationMS != 1500 {
		t.Fatalf("shell payload = %+v", ev)
	}
	if !reflect.DeepEqual(ev.Output, []string{"first line", "FAIL dyna-agent/internal/room"}) {
		t.Fatalf("shell output lines = %#v", ev.Output)
	}
	if ev.Parent != f.lead.ID {
		t.Fatalf("shell parent = %q, want %q", ev.Parent, f.lead.ID)
	}
	// The session hears it alone: a command the session ran is the session's own,
	// so its result lives in that session's DM and the room feed carries no copy.
	if !reflect.DeepEqual(res.Message.To, []string{f.lead.Address()}) || res.Message.OwnerReport {
		t.Fatalf("shell recipients = %v, owner report = %v", res.Message.To, res.Message.OwnerReport)
	}
	if home := room.HomeFor(res.Message, f.a.Name, ""); home != room.DMConversation(f.lead.ID) {
		t.Fatalf("shell home = %q, want the session DM %q", home, room.DMConversation(f.lead.ID))
	}
	if !res.Message.Wake || wakesRoot(res.Message, f.a.Name) {
		t.Fatalf("a child's shell result must wake the child and not root: wake %v", res.Message.Wake)
	}
	// The agent reads the same facts plus the output and the pointer.
	for _, want := range []string{"Shell job go · run the room tests failed with exit code 2.", "FAIL dyna-agent/internal/room", "Full output: dyna shell tail " + id} {
		if !strings.Contains(res.Message.Text, want) {
			t.Fatalf("shell text lacks %q:\n%s", want, res.Message.Text)
		}
	}
	// A retry of the same job posts nothing: the result belongs to the job.
	if again, err := f.svc.CompleteShell(f.ctx, ShellExit{ID: id, RoomID: f.a.ID, Code: 2}); err != nil || again.Message.ID != "" {
		t.Fatalf("retried shell result = %+v, %v", again.Message, err)
	}
}

// A shell the root started is owed to root: its result wakes the root seat.
func TestRootOwnedShellResultWakesRoot(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.CompleteShell(f.ctx, ShellExit{ID: "sh_20261023-120000-0000000b", RoomID: f.a.ID, Command: "make", Code: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Message.OwnerReport || !reflect.DeepEqual(res.Message.To, []string{f.a.Name}) {
		t.Fatalf("root-owned shell = %+v", res.Message)
	}
	if !wakesRoot(res.Message, f.a.Name) || !res.Message.Wake {
		t.Fatal("a root-owned shell result must wake root")
	}
	ev, ok := res.Message.Shell()
	if !ok || ev.State != room.ShellCompleted || ev.ExitCode != 0 || ev.Parent != "" {
		t.Fatalf("shell payload = %+v, %v", ev, ok)
	}
}

// A turn end wakes the parent, and root only for the failures and stops it is
// owed. A completed turn is quiet for root.
func TestTurnEventsWakeTheOwnerOnlyWhenOwed(t *testing.T) {
	for _, tc := range []struct {
		state    room.TurnState
		ownerMap bool
		wakes    bool
	}{
		{room.TurnCompleted, true, false},
		{room.TurnFailed, true, true},
		{room.TurnStopped, true, true},
		{room.TurnCompleted, false, false},
		{room.TurnFailed, false, false},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			e := newTestEnv(t)
			rec := e.room()
			store := e.svc.Rooms
			ev := room.TurnEvent{Session: rec.Name + ":decoder", SessionID: "ses_decoder", Turn: 1, State: tc.state, Summary: "The report."}
			if tc.state == room.TurnFailed {
				ev.Error = "the provider refused the request"
			}
			msg, err := store.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: ev, To: []string{rec.Name}, OwnerReport: tc.ownerMap})
			if err != nil {
				t.Fatal(err)
			}
			if got := wakesRoot(msg, rec.Name); got != tc.wakes {
				t.Fatalf("root wake = %v, want %v", got, tc.wakes)
			}
			stored, ok := msg.TurnResult()
			if !ok || stored.State != tc.state || stored.SessionID != "ses_decoder" || stored.Turn != 1 {
				t.Fatalf("stored turn payload = %+v, %v", stored, ok)
			}
			if tc.state == room.TurnCompleted && (!msg.Deliverable() || !msg.Wake) {
				t.Fatal("a completed turn is still input for its parent")
			}
		})
	}
}

// A failed turn reports its cause as the event's error and the worker's
// stderr only as detail, so a harmless harness warning never poses as the
// reason a turn failed (D-108).
func TestFailedTurnReportsWorkerStderrAsDetailNotCause(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	lead := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &lead)
	warning := `Warning: Model "muse-wonder-full-1.0" not found for provider "meta". Using custom model id.`
	_, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) {
		turn.Status, turn.Profile, turn.Model = subagent.TurnError, "pi", "muse-wonder-full-1.0"
		turn.Error = "worker pi (model muse-wonder-full-1.0) failed to start: no harness event within 2m0s of startup"
		turn.WorkerStderr = warning
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := res.Message.TurnResult()
	if !ok || ev.State != room.TurnFailed {
		t.Fatalf("failed turn event = %+v, %v", ev, ok)
	}
	if !strings.Contains(ev.Error, "no harness event within 2m0s of startup") || strings.Contains(ev.Error, warning) {
		t.Fatalf("failed turn cause = %q", ev.Error)
	}
	if ev.WorkerStderr != warning {
		t.Fatalf("failed turn lost its typed stderr evidence: %q", ev.WorkerStderr)
	}
	if !strings.Contains(ev.Detail, "Worker stderr: "+warning) {
		t.Fatalf("failed turn detail lost the worker stderr: %q", ev.Detail)
	}
}

// wakesRoot reports whether the root seat takes the post as a wake: the
// registry classifies it at write time, and this is the same decision.
func wakesRoot(msg room.Message, roomName string) bool {
	decision, _ := room.RootContext(msg, roomName)
	return decision == room.RootWake
}

// Two sessions can share one address in turn: the first closes, and a later
// session takes its name. Each first-turn report is its own post, because the
// plan keys it by session ID and turn, never by the address.
func TestTurnReportsOfTwoSessionsWithOneAddressKeepTheirPosts(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	first := e.start(rec, "decoder", &parent)
	before, err := e.svc.CompleteSessionTurn(e.ctx, first.ID, 1)
	if err != nil || before.Message.ID == "" {
		t.Fatalf("first report = %+v, %v", before.Message, err)
	}
	if _, err := e.mgr.CloseWithOptions(e.ctx, first.ID, subagent.CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	second := e.start(rec, "decoder", &parent)
	if second.Address() != first.Address() || second.ID == first.ID {
		t.Fatalf("the fixture did not reuse the address: %q and %q", first.Address(), second.Address())
	}
	after, err := e.svc.CompleteSessionTurn(e.ctx, second.ID, 1)
	if err != nil || after.Message.ID == "" {
		t.Fatalf("second session's report never reached its parent: %+v, %v", after.Message, err)
	}
	if after.Message.ID == before.Message.ID {
		t.Fatalf("both reports kept one post ID: %s", after.Message.ID)
	}
	for _, res := range []Result{before, after} {
		ev, ok := res.Message.TurnResult()
		if !ok || ev.Session != first.Address() || ev.Turn != 1 {
			t.Fatalf("stored report = %+v, %v", ev, ok)
		}
	}
	if firstEv, _ := before.Message.TurnResult(); firstEv.SessionID != first.ID {
		t.Fatalf("first report names session %q, want %q", firstEv.SessionID, first.ID)
	}
	if secondEv, _ := after.Message.TurnResult(); secondEv.SessionID != second.ID {
		t.Fatalf("second report names session %q, want %q", secondEv.SessionID, second.ID)
	}
}
