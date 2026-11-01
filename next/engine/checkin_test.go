package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

// goalRig is a real store and service with a clock the test moves: root, a lead under it, and a child under the lead
// that holds one goal. The lead holds none, so each test adds only what it is about.
type goalRig struct {
	t                 *testing.T
	s                 *Service
	st                *store.Store
	dir               string
	ctx               context.Context
	clock             *atomic.Int64
	room              contract.RoomID
	root, lead, child contract.SessionID
	turns             int
}

var ownerCaller = contract.Principal{Kind: contract.PrincipalOwner}

func newGoalRig(t *testing.T) *goalRig {
	t.Helper()
	r := &goalRig{t: t, dir: t.TempDir(), ctx: context.Background(), clock: &atomic.Int64{}}
	r.clock.Store(time.Unix(100000, 0).UnixMilli())
	r.open()
	t.Cleanup(func() {
		if err := r.st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.st.AddHost(r.ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: r.now()}); err != nil {
		t.Fatal(err)
	}
	created, e := r.s.RoomCreate(r.ctx, ownerCaller, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	r.room = created.Room
	lead, e := r.s.SessionStart(r.ctx, ownerCaller, contract.SessionStartRequest{Room: r.room, Name: "lead", Profile: "test", Brief: "lead the work"})
	if e != nil {
		t.Fatal(e)
	}
	child, e := r.s.SessionStart(r.ctx, asSession(lead.Session), contract.SessionStartRequest{Room: r.room, Name: "child", Profile: "test", Goal: "ship the port", DoneWhen: "the port is merged"})
	if e != nil {
		t.Fatal(e)
	}
	r.lead, r.child = lead.Session, child.Session
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT parent FROM sessions WHERE id=?", r.lead).Scan(&r.root); err != nil {
		t.Fatal(err)
	}
	r.exec("UPDATE boxes SET state='ready',want='run'")
	r.turn(r.lead, "started the child")
	r.turn(r.root, "noted")
	return r
}

func (r *goalRig) open() {
	r.t.Helper()
	st, err := store.Open(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	r.st = st
	r.s = New(st, fakeBoxes{}, fakeProfiles{}, r.now)
}

// restart is an engine restart: the store closes, and a new service opens the same folder.
func (r *goalRig) restart() {
	r.t.Helper()
	if err := r.st.Close(); err != nil {
		r.t.Fatal(err)
	}
	r.open()
}

func (r *goalRig) now() time.Time { return time.UnixMilli(r.clock.Load()) }

func (r *goalRig) advance(d time.Duration) { r.clock.Add(d.Milliseconds()) }

func (r *goalRig) exec(query string, args ...any) {
	r.t.Helper()
	if err := r.st.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.ctx, query, args...)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
}

// begin dispatches a session's waiting inputs into a new turn, which stays open.
func (r *goalRig) begin(session contract.SessionID) contract.TurnID {
	r.t.Helper()
	r.turns++
	id := contract.TurnID("turn_" + string(rune('a'+r.turns)))
	started, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		r.t.Fatal(err)
	}
	active, _, _, err := r.st.Dispatch(r.ctx, session, id, started, false)
	if err != nil || active != id {
		r.t.Fatalf("dispatch of %s: %q, %v", session, active, err)
	}
	return id
}

func (r *goalRig) end(session contract.SessionID, turn contract.TurnID, outcome contract.TurnOutcome, cause contract.FailureCause, final string) {
	r.t.Helper()
	request := contract.TurnOutputRequest{Turn: turn, Outcome: outcome, Cause: cause, Final: final}
	if outcome == contract.TurnFailed {
		request.Error = "the turn broke"
	}
	if _, e := r.s.TurnOutput(r.ctx, asSession(session), request); e != nil {
		r.t.Fatal(e)
	}
}

// turn runs one completed turn over whatever waits for the session.
func (r *goalRig) turn(session contract.SessionID, final string) {
	r.t.Helper()
	r.end(session, r.begin(session), contract.TurnCompleted, "", final)
}

// notice gives a session an engine notice that owes no reply, as a finished job would.
func (r *goalRig) notice(session contract.SessionID) {
	r.t.Helper()
	event, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.ShellJobEnded{Session: session, Job: "job_1"})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.Post(r.ctx, event, []contract.Delivery{{To: asSession(session), Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *goalRig) pass() {
	r.t.Helper()
	if err := r.s.FailureNotices(r.ctx); err != nil {
		r.t.Fatal(err)
	}
	if err := r.s.Checkins(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

func eventBodies[T any](r *goalRig, kind contract.EventType, to contract.SessionID) []T {
	r.t.Helper()
	rows, err := r.st.Read().QueryContext(r.ctx, "SELECT e.body FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_kind='session' AND d.to_id=? AND e.type=? ORDER BY d.seq", to, kind)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var data []byte
		var body T
		if err := rows.Scan(&data); err != nil {
			r.t.Fatal(err)
		}
		if err := json.Unmarshal(data, &body); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, body)
	}
	return out
}

func (r *goalRig) checkins(to contract.SessionID) []contract.CheckinDue {
	r.t.Helper()
	return eventBodies[contract.CheckinDue](r, "checkin.due", to)
}

func (r *goalRig) failures(to contract.SessionID) []contract.SessionFailed {
	r.t.Helper()
	return eventBodies[contract.SessionFailed](r, "session.failed", to)
}

func (r *goalRig) wantCheckins(to contract.SessionID, want int) {
	r.t.Helper()
	if got := r.checkins(to); len(got) != want {
		r.t.Fatalf("%d check-ins, want %d: %+v", len(got), want, got)
	}
}

func (r *goalRig) wantFailures(to contract.SessionID, want int) {
	r.t.Helper()
	if got := r.failures(to); len(got) != want {
		r.t.Fatalf("%d failure notices, want %d: %+v", len(got), want, got)
	}
}

func TestStalledChildReachesParentThenRootAndStops(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")

	r.advance(room.CheckinIdle - time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 0)

	r.advance(2 * time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	r.wantCheckins(r.root, 0)
	report := r.checkins(r.lead)[0].Children
	if len(report) != 1 || report[0].Session != r.child || report[0].State != contract.CheckinIdle || report[0].Goal != "g1" || report[0].Text != "ship the port" {
		t.Fatalf("report %+v", report)
	}
	var wake bool
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='checkin.due'", r.lead).Scan(&wake); err != nil || !wake {
		t.Fatalf("wake %v: %v", wake, err)
	}

	// The lead reads it and does nothing. The same stall does not come back to it.
	r.turn(r.lead, "not now")
	r.advance(room.CheckinInterval + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	r.wantCheckins(r.root, 0)

	r.advance(room.CheckinEscalate)
	r.pass()
	r.wantCheckins(r.root, 1)
	r.wantCheckins(r.lead, 1)
	if up := r.checkins(r.root)[0].Children; len(up) != 1 || up[0].Parent != "lead" {
		t.Fatalf("escalated report %+v", up)
	}

	// Root has it; nobody hears of it again.
	r.turn(r.root, "looking")
	r.advance(24 * time.Hour)
	r.pass()
	r.wantCheckins(r.root, 1)
	r.wantCheckins(r.lead, 1)
}

func TestGoalProgressStartsTheStallOver(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	r.turn(r.lead, "fine")

	if _, e := r.s.GoalJournal(r.ctx, asSession(r.child), contract.GoalJournalRequest{Goal: "g1", Text: "waiting on review"}); e != nil {
		t.Fatal(e)
	}
	r.advance(room.CheckinIdle - time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	r.advance(room.CheckinInterval)
	r.pass()
	r.wantCheckins(r.lead, 2)
	r.wantCheckins(r.root, 0)
}

func TestEndedGoalStopsReports(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "done")
	r.turn(r.lead, "good")
	if _, e := r.s.GoalDone(r.ctx, asSession(r.child), contract.GoalDoneRequest{Goal: "g1", Summary: "merged"}); e != nil {
		t.Fatal(e)
	}
	r.advance(48 * time.Hour)
	r.pass()
	r.wantCheckins(r.lead, 0)
	r.wantCheckins(r.root, 0)
}

func TestCheckinWakesAStoppedParent(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.exec("UPDATE boxes SET state='stopped',want='stop' WHERE session=?", r.lead)
	r.exec("UPDATE boxes SET state='stopped',want='stop' WHERE session=?", r.child)
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	got := r.checkins(r.lead)
	if len(got) != 1 || got[0].Children[0].State != contract.CheckinStopped {
		t.Fatalf("check-ins %+v", got)
	}
	var want string
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT want FROM boxes WHERE session=?", r.lead).Scan(&want); err != nil || want != "run" {
		t.Fatalf("parent box want %q: %v", want, err)
	}
}

// A parent stuck in one turn gets the report as a steer, gets no second one on top, and cannot hold the stall back from root.
func TestBusyParentCannotHoldAStallBack(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	hung := r.begin(r.lead)
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)

	started, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	active, inputs, _, err := r.st.Dispatch(r.ctx, r.lead, "turn_unused", started, false)
	if err != nil || active != hung || len(inputs) == 0 || inputs[len(inputs)-1].Queue == nil || *inputs[len(inputs)-1].Queue != contract.QueueSteer {
		t.Fatalf("steer: active %q, inputs %+v: %v", active, inputs, err)
	}

	second, e := r.s.SessionStart(r.ctx, asSession(r.lead), contract.SessionStartRequest{Room: r.room, Name: "second", Profile: "test", Goal: "second goal"})
	if e != nil {
		t.Fatal(e)
	}
	r.exec("UPDATE boxes SET state='ready' WHERE session=?", second.Session)
	r.turn(second.Session, "started")
	r.advance(room.CheckinInterval + room.CheckinIdle)
	r.pass()
	r.wantCheckins(r.lead, 1)

	r.advance(room.CheckinEscalate)
	r.pass()
	r.wantCheckins(r.lead, 1)
	got := r.checkins(r.root)
	if len(got) != 1 || len(got[0].Children) != 2 {
		t.Fatalf("root check-ins %+v", got)
	}
}

func TestPausedRoomGetsNoReportsUntilResumed(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	if _, e := r.s.RoomPause(r.ctx, ownerCaller, contract.RoomPauseRequest{Room: r.room, Paused: true}); e != nil {
		t.Fatal(e)
	}
	r.exec("UPDATE boxes SET state='lost' WHERE session=?", r.child)
	r.advance(6 * time.Hour)
	r.pass()
	r.wantCheckins(r.lead, 0)
	r.wantCheckins(r.root, 0)
	r.wantFailures(r.lead, 0)
	if _, e := r.s.RoomPause(r.ctx, ownerCaller, contract.RoomPauseRequest{Room: r.room, Paused: false}); e != nil {
		t.Fatal(e)
	}
	r.pass()
	r.wantFailures(r.lead, 1)
}

func TestRestartKeepsTheClocks(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	r.turn(r.lead, "fine")

	r.restart()
	r.pass()
	r.advance(room.CheckinInterval + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	r.wantCheckins(r.root, 0)

	r.restart()
	r.advance(room.CheckinEscalate)
	r.pass()
	r.wantCheckins(r.root, 1)
	r.wantCheckins(r.lead, 1)
}

func TestSilentTurnIsVisibleToItsParent(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.advance(room.CheckinInterval + time.Minute)
	r.notice(r.child)
	r.turn(r.child, "ok")
	r.pass()
	got := r.checkins(r.lead)
	if len(got) != 1 || !got[0].Children[0].Silent {
		t.Fatalf("check-ins %+v", got)
	}
}

func TestPostedOrRepliedTurnIsNotSilent(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.pass()
	r.wantCheckins(r.lead, 0)

	turnID := func() contract.TurnID {
		r.notice(r.child)
		return r.begin(r.child)
	}
	turn := turnID()
	if _, e := r.s.Post(r.ctx, asSession(r.child), contract.PostRequest{Room: r.room, Text: "status: tests run", Home: contract.Conversation{Kind: contract.ConversationFeed}}); e != nil {
		t.Fatal(e)
	}
	r.end(r.child, turn, contract.TurnCompleted, "", "ok")
	r.pass()
	r.wantCheckins(r.lead, 0)
}

func TestQuestionForTheOwnerIsNotAStall(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "asking")
	r.turn(r.lead, "good")
	r.exec("INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q_1',?,?,'which one?','pending',?)", r.room, r.child, r.now().UnixMilli())
	r.advance(24 * time.Hour)
	r.pass()
	r.wantCheckins(r.lead, 0)
	r.wantCheckins(r.root, 0)
	// Answered, it is an idle goal holder again.
	r.exec("UPDATE questions SET state='answered'")
	r.pass()
	r.wantCheckins(r.lead, 1)
}

func TestLeadWithWorkingChildIsNotStalled(t *testing.T) {
	r := newGoalRig(t)
	if _, e := r.s.GoalSet(r.ctx, asSession(r.root), contract.GoalSetRequest{Session: r.lead, Text: "lead it", DoneWhen: "all merged"}); e != nil {
		t.Fatal(e)
	}
	r.turn(r.lead, "on it")
	r.turn(r.root, "good")
	r.begin(r.child)
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.root, 0)
	r.wantCheckins(r.lead, 0)
}

func TestBoxLossTellsTheParentAtOnceAndOnce(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.exec("UPDATE boxes SET state='lost',reason='box disconnected' WHERE session=?", r.child)
	r.pass()
	got := r.failures(r.lead)
	if len(got) != 1 || len(got[0].Failures) != 1 || got[0].Failures[0].Box != contract.BoxLost || got[0].Failures[0].Reason != "box disconnected" {
		t.Fatalf("notices %+v", got)
	}
	r.pass()
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantFailures(r.lead, 1)
	r.wantCheckins(r.lead, 0) // The notice already told this ancestor.
	r.turn(r.lead, "looking at it")

	// It failed for good: same failure, no second notice. Time still takes the stall up, because it holds a goal.
	r.exec("UPDATE boxes SET state='failed' WHERE session=?", r.child)
	r.pass()
	r.wantFailures(r.lead, 1)
	r.advance(room.CheckinEscalate)
	r.pass()
	r.wantCheckins(r.root, 1)

	// It recovers and fails again: a new failure.
	r.exec("UPDATE boxes SET state='ready' WHERE session=?", r.child)
	r.pass()
	r.exec("UPDATE boxes SET state='lost' WHERE session=?", r.child)
	r.pass()
	r.wantFailures(r.lead, 2)
}

func TestBoxLossWithNoGoalStillTellsTheParent(t *testing.T) {
	r := newGoalRig(t)
	r.exec("UPDATE boxes SET state='lost' WHERE session=?", r.lead)
	r.pass()
	r.wantFailures(r.root, 1)
}

func TestFailureGoesPastAParentThatCannotTakeIt(t *testing.T) {
	r := newGoalRig(t)
	r.exec("UPDATE boxes SET state='lost' WHERE session IN (?,?)", r.lead, r.child)
	r.pass()
	r.wantFailures(r.lead, 0)
	got := r.failures(r.root)
	if len(got) != 1 || len(got[0].Failures) != 2 {
		t.Fatalf("root notices %+v", got)
	}
	for _, failure := range got[0].Failures {
		if failure.Session == r.child && failure.Parent != "lead" {
			t.Fatalf("failure %+v", failure)
		}
	}
}

func TestFailedTurnNoticeIsImmediateAndCoversItsRetries(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.notice(r.child)
	r.end(r.child, r.begin(r.child), contract.TurnFailed, contract.CauseModelFailed, "")
	r.pass()
	got := r.failures(r.lead)
	if len(got) != 1 || got[0].Failures[0].Cause != contract.CauseModelFailed || got[0].Failures[0].Reason != "the turn broke" {
		t.Fatalf("notices %+v", got)
	}
	// The retry fails too: the same failure.
	r.exec("UPDATE deliveries SET wake=1 WHERE to_id=? AND state='pending'", r.child)
	r.end(r.child, r.begin(r.child), contract.TurnFailed, contract.CauseModelFailed, "")
	r.pass()
	r.wantFailures(r.lead, 1)
	// It recovers, then fails again.
	r.exec("UPDATE deliveries SET wake=1 WHERE to_id=? AND state='pending'", r.child)
	r.turn(r.child, "better")
	r.turn(r.lead, "good")
	r.pass()
	r.notice(r.child)
	r.end(r.child, r.begin(r.child), contract.TurnFailed, contract.CauseHarnessFailed, "")
	r.pass()
	r.wantFailures(r.lead, 2)
}

// The brief's own reply channel already carries a failure to the parent, so no second notice follows it.
func TestFailedTurnThatRepliedIsNotToldTwice(t *testing.T) {
	r := newGoalRig(t)
	r.end(r.child, r.begin(r.child), contract.TurnFailed, contract.CauseHarnessFailed, "")
	r.pass()
	r.wantFailures(r.lead, 0)
	var delivered int
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT COUNT(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id=? AND e.type='turn.ended' AND d.state='pending'", r.lead).Scan(&delivered); err != nil || delivered != 1 {
		t.Fatalf("turn failure deliveries %d: %v", delivered, err)
	}
}

func TestLostBoxTurnDoesNotLookLive(t *testing.T) {
	r := newGoalRig(t)
	turn := r.begin(r.child)
	row := func() contract.SessionSummary {
		t.Helper()
		list, e := r.s.SessionList(r.ctx, asSession(r.lead), contract.SessionListRequest{Room: r.room})
		if e != nil {
			t.Fatal(e)
		}
		for _, session := range list.Sessions {
			if session.ID == r.child {
				return session
			}
		}
		t.Fatal("child not listed")
		return contract.SessionSummary{}
	}
	if got := row(); got.Turn != turn {
		t.Fatalf("running turn %q", got.Turn)
	}
	r.exec("UPDATE boxes SET state='lost' WHERE session=?", r.child)
	if got := row(); got.Turn != "" || got.BoxState != contract.BoxLost {
		t.Fatalf("lost box listed %+v", got)
	}
	// The stall logic reads the box first, so the open turn cannot excuse the session or its parent.
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantFailures(r.lead, 1)
	// A box that comes back carries on with the same turn.
	r.exec("UPDATE boxes SET state='ready' WHERE session=?", r.child)
	if got := row(); got.Turn != turn {
		t.Fatalf("returned box listed %+v", got)
	}
}

func TestGoalAuthority(t *testing.T) {
	r := newGoalRig(t)
	sibling, e := r.s.SessionStart(r.ctx, asSession(r.root), contract.SessionStartRequest{Room: r.room, Name: "sibling", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	stranger, e := r.s.RoomCreate(r.ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	var strangerRoot contract.SessionID
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT id FROM sessions WHERE room=?", stranger.Room).Scan(&strangerRoot); err != nil {
		t.Fatal(err)
	}
	const goal = contract.GoalID("g1") // held by child, under lead, under root
	item := contract.GoalItemRequest{Goal: goal, Text: "a step"}
	journal := contract.GoalJournalRequest{Goal: goal, Text: "a note"}
	done := contract.GoalDoneRequest{Goal: goal, Summary: "over", Drop: true}
	edit := contract.GoalSetRequest{Goal: goal, Text: "changed"}
	try := func(caller contract.Principal, roomID contract.RoomID) map[string]*contract.Error {
		out := map[string]*contract.Error{}
		i, j, d, s := item, journal, done, edit
		i.Room, j.Room, d.Room, s.Room = roomID, roomID, roomID, roomID
		_, out["item"] = r.s.GoalItem(r.ctx, caller, i)
		_, out["journal"] = r.s.GoalJournal(r.ctx, caller, j)
		_, out["set"] = r.s.GoalSet(r.ctx, caller, s)
		_, out["done"] = r.s.GoalDone(r.ctx, caller, d)
		return out
	}
	for name, caller := range map[string]contract.Principal{"sibling": asSession(sibling.Session), "engine": {Kind: contract.PrincipalEngine}} {
		for action, problem := range try(caller, r.room) {
			if problem == nil || problem.Code != contract.ErrForbidden {
				t.Fatalf("%s %s: %v", name, action, problem)
			}
		}
	}
	for action, problem := range try(asSession(strangerRoot), r.room) {
		if problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("other room %s: %v", action, problem)
		}
	}
	// Named from another room, the same number is another goal: the caller's own room has none.
	for action, problem := range try(asSession(strangerRoot), "") {
		if problem == nil || problem.Code != contract.ErrNotFound {
			t.Fatalf("own room %s: %v", action, problem)
		}
	}
	// A child cannot touch its parent's goal, and nobody gives work upward.
	lead, e := r.s.GoalSet(r.ctx, ownerCaller, contract.GoalSetRequest{Room: r.room, Session: r.lead, Text: "lead it", DoneWhen: "merged"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: lead.Goal, Text: "x"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("child on parent's goal: %v", e)
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(r.child), contract.GoalSetRequest{Session: r.lead, Text: "x", DoneWhen: "y"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("goal for a parent: %v", e)
	}
	// Root holds no goals, whoever asks, and the refusal says what to do.
	for _, caller := range []contract.Principal{asSession(r.root), ownerCaller} {
		req := contract.GoalSetRequest{Room: r.room, Session: r.root, Text: "x", DoneWhen: "y"}
		if _, e := r.s.GoalSet(r.ctx, caller, req); e == nil || e.Code != contract.ErrInvalid || !strings.Contains(e.Message, "root holds no goals") {
			t.Fatalf("root holder: %v", e)
		}
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(r.root), contract.GoalSetRequest{Text: "x", DoneWhen: "y"}); e == nil || !strings.Contains(e.Message, "root holds no goals") {
		t.Fatalf("root's own goal: %v", e)
	}
	// The holder, an ancestor and the owner may all work on it.
	for _, caller := range []contract.Principal{asSession(r.child), asSession(r.lead), asSession(r.root), ownerCaller} {
		if _, e := r.s.GoalItem(r.ctx, caller, contract.GoalItemRequest{Room: r.room, Goal: goal, Text: "a step"}); e != nil {
			t.Fatalf("%+v: %v", caller, e)
		}
	}
	if _, e := r.s.GoalDone(r.ctx, asSession(r.child), contract.GoalDoneRequest{Goal: goal, Summary: "done"}); e == nil || e.Code != contract.ErrInvalid || !strings.Contains(e.Message, "4 open items") {
		t.Fatalf("done with open items: %v", e)
	}
	if _, e := r.s.GoalDone(r.ctx, ownerCaller, contract.GoalDoneRequest{Room: r.room, Goal: goal, Summary: "not needed", Drop: true}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: goal, Text: "late"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("item on an ended goal: %v", e)
	}
}

func TestGoalLifecycleThroughTheAPI(t *testing.T) {
	r := newGoalRig(t)
	child := asSession(r.child)
	own, e := r.s.GoalSet(r.ctx, child, contract.GoalSetRequest{ClientID: "retry", Text: "write tests", DoneWhen: "tests pass"})
	if e != nil {
		t.Fatal(e)
	}
	again, e := r.s.GoalSet(r.ctx, child, contract.GoalSetRequest{ClientID: "retry", Text: "write tests", DoneWhen: "tests pass"})
	if e != nil || again.Goal != own.Goal {
		t.Fatalf("retry: %+v %v", again, e)
	}
	first, e := r.s.GoalItem(r.ctx, child, contract.GoalItemRequest{Goal: own.Goal, Text: "unit"})
	if e != nil || first.Item != 1 {
		t.Fatalf("item %+v: %v", first, e)
	}
	if _, e := r.s.GoalItem(r.ctx, child, contract.GoalItemRequest{Goal: own.Goal, Item: 1, State: contract.GoalItemDone}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.GoalItem(r.ctx, child, contract.GoalItemRequest{Goal: own.Goal, Item: 9, State: contract.GoalItemDone}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("missing item: %v", e)
	}
	if _, e := r.s.GoalItem(r.ctx, child, contract.GoalItemRequest{Goal: own.Goal, Item: 1, State: "maybe"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("bad state: %v", e)
	}
	if _, e := r.s.GoalJournal(r.ctx, child, contract.GoalJournalRequest{Goal: own.Goal, Text: "first"}); e != nil {
		t.Fatal(e)
	}
	journal, e := r.s.GoalJournal(r.ctx, child, contract.GoalJournalRequest{Goal: own.Goal, Text: "second"})
	if e != nil || len(journal.Entries) != 2 || journal.Entries[0].Text != "second" || journal.Entries[0].By.Session != r.child {
		t.Fatalf("journal %+v: %v", journal, e)
	}
	if _, e := r.s.GoalDone(r.ctx, child, contract.GoalDoneRequest{Goal: own.Goal, Summary: "all pass"}); e != nil {
		t.Fatal(e)
	}
	list, e := r.s.GoalList(r.ctx, asSession(r.root), contract.GoalListRequest{})
	if e != nil || len(list.Goals) != 1 || list.Goals[0].ID != "g1" || list.Goals[0].DoneWhen != "the port is merged" || list.Goals[0].Session != r.child {
		t.Fatalf("active goals %+v: %v", list, e)
	}
	all, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room, All: true, Session: r.child})
	if e != nil || len(all.Goals) != 2 || all.Goals[1].State != contract.GoalDone || all.Goals[1].Summary != "all pass" {
		t.Fatalf("all goals %+v: %v", all, e)
	}
	ended := all.Goals[1]
	if len(ended.Items) != 1 || ended.Items[0].State != contract.GoalItemDone {
		t.Fatalf("items %+v", ended.Items)
	}
}

func TestGoalHandoverWakesTheNewHolder(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "started")
	r.turn(r.lead, "good")
	if _, e := r.s.GoalSet(r.ctx, asSession(r.lead), contract.GoalSetRequest{Goal: "g1", Text: "ship the port, then the docs", Session: r.child}); e != nil {
		t.Fatal(e)
	}
	if got := eventBodies[contract.GoalSet](r, "goal.set", r.child); len(got) != 0 {
		t.Fatalf("an edit woke its holder: %+v", got)
	}
	second, e := r.s.SessionStart(r.ctx, asSession(r.lead), contract.SessionStartRequest{Room: r.room, Name: "second", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(r.lead), contract.GoalSetRequest{Goal: "g1", Session: second.Session}); e != nil {
		t.Fatal(e)
	}
	got := eventBodies[contract.GoalSet](r, "goal.set", second.Session)
	if len(got) != 1 || got[0].Text != "ship the port, then the docs" || got[0].DoneWhen != "the port is merged" {
		t.Fatalf("handover %+v", got)
	}
}

func closeForTest(r *goalRig, id contract.SessionID) {
	r.t.Helper()
	event, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: id})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.CloseSession(r.ctx, id, event); err != nil {
		r.t.Fatal(err)
	}
}

func TestClosedSessionsGoalsGoToItsParentButNotToRoot(t *testing.T) {
	r := newGoalRig(t)
	closeForTest(r, r.child)
	goals, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room})
	if e != nil || len(goals.Goals) != 1 || goals.Goals[0].Session != r.lead {
		t.Fatalf("after the child closed: %+v %v", goals, e)
	}
	closeForTest(r, r.lead)
	goals, e = r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room})
	if e != nil || len(goals.Goals) != 1 || goals.Goals[0].Session != "" {
		t.Fatalf("after the lead closed: %+v %v", goals, e)
	}
	closed := eventBodies[contract.SessionClosed](r, "session.closed", r.root)
	if len(closed) != 1 || closed[0].Session != r.lead || len(closed[0].Unheld) != 1 || closed[0].Unheld[0] != "g1" {
		t.Fatalf("root's notice: %+v", closed)
	}

	// Only root or the owner can pick up an unheld goal, and it wakes the new holder.
	worker, e := r.s.SessionStart(r.ctx, ownerCaller, contract.SessionStartRequest{Room: r.room, Name: "worker", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(worker.Session), contract.GoalSetRequest{Goal: "g1", Session: worker.Session}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("worker took an unheld goal: %v", e)
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(r.root), contract.GoalSetRequest{Goal: "g1", Session: worker.Session}); e != nil {
		t.Fatal(e)
	}
	if got := eventBodies[contract.GoalSet](r, "goal.set", worker.Session); len(got) != 1 {
		t.Fatalf("new holder's deliveries: %+v", got)
	}
}

func TestDormantCloseSkipsOpenGoalsAndDiskEvictionDoesNot(t *testing.T) {
	r := newGoalRig(t)
	if _, e := r.s.GoalDone(r.ctx, ownerCaller, contract.GoalDoneRequest{Room: r.room, Goal: "g1", Summary: "moved", Drop: true}); e != nil {
		t.Fatal(e)
	}
	second, e := r.s.SessionStart(r.ctx, ownerCaller, contract.SessionStartRequest{Room: r.room, Name: "second", Profile: "test", Parent: r.lead, Goal: "still open"})
	if e != nil {
		t.Fatal(e)
	}
	r.exec("UPDATE deliveries SET state='read'")
	r.exec("UPDATE boxes SET state='stopped',want='stop',updated_at=0 WHERE session IN (?,?)", r.child, second.Session)
	r.advance(30 * 24 * time.Hour)
	ids, err := r.st.DormantCloseCandidates(r.ctx, r.now().Add(-7*24*time.Hour))
	if err != nil || len(ids) != 1 || ids[0] != r.child {
		t.Fatalf("candidates %v: %v", ids, err)
	}
	changed, err := r.st.MarkDormantClosing(r.ctx, second.Session, r.now().Add(-7*24*time.Hour), r.now().Add(-diskLowWait))
	if err != nil || changed {
		t.Fatalf("closed a session with an open goal: %v %v", changed, err)
	}
	r.exec("UPDATE boxes SET host='host' WHERE session=?", second.Session)
	r.exec("UPDATE hosts SET disk_bytes=0,disk_low_since=1")
	ids, err = r.st.DiskEvictCandidates(r.ctx, r.now())
	if err != nil || len(ids) == 0 {
		t.Fatalf("eviction candidates %v: %v", ids, err)
	}
}

func TestGoalContextSections(t *testing.T) {
	r := newGoalRig(t)
	for _, text := range []string{"first step", "second step"} {
		if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: "g1", Text: text}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: "g1", Item: 1, State: contract.GoalItemDone}); e != nil {
		t.Fatal(e)
	}
	section := func(caller contract.SessionID, key contract.ContextSectionKey) string {
		t.Helper()
		got, e := r.s.ContextSnapshot(r.ctx, asSession(caller), contract.ContextSnapshotRequest{Section: key})
		if e != nil {
			t.Fatal(e)
		}
		if len(got.Sections) == 0 {
			return ""
		}
		return got.Sections[0].Text
	}
	if got, want := section(r.child, contract.ContextGoal), "g1: ship the port\nDone when: the port is merged\n- second step"; got != want {
		t.Fatalf("dyna_goal %q", got)
	}
	if got := section(r.root, contract.ContextGoal); got != "" {
		t.Fatalf("root's dyna_goal %q", got)
	}
	if got := section(r.lead, contract.ContextChildren); !strings.Contains(got, "second step") {
		t.Fatalf("dyna_children %q", got)
	}
	for i := 0; i < 40; i++ {
		if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: "g1", Text: strings.Repeat("long item ", 30)}); e != nil {
			t.Fatal(e)
		}
	}
	snapshot, e := r.s.ContextSnapshot(r.ctx, asSession(r.child), contract.ContextSnapshotRequest{})
	if e != nil || snapshot.Sections[0].Key != contract.ContextGoal {
		t.Fatalf("snapshot %+v: %v", snapshot, e)
	}
	capped := snapshot.Sections[0].Text
	if len(capped) > 800+16 || !strings.Contains(capped, "more: context show") || !strings.HasPrefix(capped, "g1: ship the port") {
		t.Fatalf("capped section (%d bytes): %q", len(capped), capped)
	}
	if full := section(r.child, contract.ContextGoal); strings.Contains(full, "more: context show") || strings.Count(full, "\n- ") != 41 {
		t.Fatalf("full section: %q", full)
	}
}

func TestStartGoalIsARealGoal(t *testing.T) {
	r := newGoalRig(t)
	got, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room})
	if e != nil || len(got.Goals) != 1 {
		t.Fatalf("goals %+v: %v", got, e)
	}
	goal := got.Goals[0]
	if goal.Session != r.child || goal.Text != "ship the port" || goal.DoneWhen != "the port is merged" || goal.State != contract.GoalActive {
		t.Fatalf("goal %+v", goal)
	}
	// The room root keeps its start text, but holds no goal.
	roomRoot, e := r.s.RoomCreate(r.ctx, ownerCaller, contract.RoomCreateRequest{Name: "third", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	none, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: roomRoot.Room})
	if e != nil || len(none.Goals) != 0 {
		t.Fatalf("root goals %+v: %v", none, e)
	}
}
