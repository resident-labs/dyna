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
	if deadline, err := r.st.InputBatchDeadline(r.ctx, session); err != nil {
		r.t.Fatal(err)
	} else if deadline.After(r.now()) {
		r.advance(deadline.Sub(r.now()))
	}
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

func TestStalledChildReachesParentThenRootAndStops(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")

	r.advance(room.CheckinIdle - 2*time.Minute)
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

func TestSettledGoalChecklistDoesNotStall(t *testing.T) {
	r := newGoalRig(t)
	for i, state := range []contract.GoalItemState{contract.GoalItemDone, contract.GoalItemDropped} {
		if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: "g1", Text: "settled step"}); e != nil {
			t.Fatal(e)
		}
		if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: "g1", Item: i + 1, State: state}); e != nil {
			t.Fatal(e)
		}
	}
	r.turn(r.child, "all current steps settled; waiting for new input")
	r.turn(r.lead, "noted")
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 0)
	r.wantCheckins(r.root, 0)

	// A standing goal stays active, and adding real work restores check-ins.
	if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: "g1", Text: "new work"}); e != nil {
		t.Fatal(e)
	}
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
	if got := r.checkins(r.lead)[0].Children; len(got) != 1 || got[0].Session != r.child || got[0].Open != 1 {
		t.Fatalf("report %+v", got)
	}
}

func TestZeroItemGoalStillStalls(t *testing.T) {
	r := newGoalRig(t)
	r.turn(r.child, "work remains despite no checklist")
	r.turn(r.lead, "noted")
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
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

// askAs has a session ask the owner.
func (r *goalRig) askAs(session contract.SessionID, blocked bool) {
	r.t.Helper()
	grantAsk(r.t, r.s, r.ctx, r.room, session)
	if _, e := r.s.QuestionAsk(r.ctx, asSession(session), contract.QuestionAskRequest{Text: "which one?", Meanwhile: "the next item", Blocked: blocked}); e != nil {
		r.t.Fatal(e)
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
	if _, e := r.s.GoalItem(r.ctx, asSession(r.child), contract.GoalItemRequest{Goal: lead.Goal, Text: "x"}); e == nil || e.Code != contract.ErrForbidden || !strings.Contains(e.Message, "ask your parent") {
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
	if _, e := r.s.GoalDone(r.ctx, asSession(r.child), contract.GoalDoneRequest{Goal: goal, Summary: "done"}); e == nil || e.Code != contract.ErrInvalid || !strings.Contains(e.Message, "4 open items (1, 2, 3, 4); set each to done or dropped with goal item") {
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
	child := asSession(r.lead)
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
	if _, e := r.s.GoalItem(r.ctx, child, contract.GoalItemRequest{Goal: own.Goal, Item: 9, State: contract.GoalItemDone}); e == nil || e.Code != contract.ErrNotFound || e.Message != "goal "+string(own.Goal)+" has no item 9; goal list shows its items" {
		t.Fatalf("missing item: %v", e)
	}
	if _, e := r.s.GoalItem(r.ctx, child, contract.GoalItemRequest{Goal: own.Goal, Item: 1, State: "maybe"}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("bad state: %v", e)
	}
	if _, e := r.s.GoalJournal(r.ctx, child, contract.GoalJournalRequest{Text: "first"}); e != nil {
		t.Fatal(e)
	}
	journal, e := r.s.GoalJournal(r.ctx, child, contract.GoalJournalRequest{Goal: own.Goal, Text: "second"})
	if e != nil || len(journal.Entries) != 2 || journal.Entries[0].Text != "second" || journal.Entries[0].By.Session != r.lead {
		t.Fatalf("journal %+v: %v", journal, e)
	}
	// An omitted goal is the caller's own; a caller that holds none is told to name one.
	if _, e := r.s.GoalDone(r.ctx, asSession(r.root), contract.GoalDoneRequest{Summary: "all pass"}); e == nil || e.Message != "you hold no goal, so name one; goal list shows the room's goals" {
		t.Fatalf("done with no goal held: %v", e)
	}
	if _, e := r.s.GoalDone(r.ctx, child, contract.GoalDoneRequest{Summary: "all pass"}); e != nil {
		t.Fatal(e)
	}
	list, e := r.s.GoalList(r.ctx, asSession(r.root), contract.GoalListRequest{})
	if e != nil || len(list.Goals) != 1 || list.Goals[0].ID != "g1" || list.Goals[0].DoneWhen != "the port is merged" || list.Goals[0].Session != r.child {
		t.Fatalf("active goals %+v: %v", list, e)
	}
	all, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room, All: true, Session: r.lead})
	if e != nil || len(all.Goals) != 1 || all.Goals[0].State != contract.GoalDone || all.Goals[0].Summary != "all pass" {
		t.Fatalf("all goals %+v: %v", all, e)
	}
	ended := all.Goals[0]
	if len(ended.Items) != 1 || ended.Items[0].State != contract.GoalItemDone {
		t.Fatalf("items %+v", ended.Items)
	}
}

func TestGoalJournalReturnsTheNewestFiveAndCountsTheRest(t *testing.T) {
	r := newGoalRig(t)
	child := asSession(r.child)
	var last contract.GoalJournalResponse
	for i := 1; i <= 7; i++ {
		var e *contract.Error
		last, e = r.s.GoalJournal(r.ctx, child, contract.GoalJournalRequest{Goal: "g1", Text: strings.Repeat("x", i)})
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(last.Entries) != 5 || last.Older != 2 || last.Entries[0].Text != "xxxxxxx" || last.Entries[4].Text != "xxx" {
		t.Fatalf("journal %+v", last)
	}
	read, e := r.s.GoalJournal(r.ctx, child, contract.GoalJournalRequest{Goal: "g1"})
	if e != nil || len(read.Entries) != 5 || read.Older != 2 {
		t.Fatalf("read %+v: %v", read, e)
	}
}

func TestGoalListTakesASessionName(t *testing.T) {
	r := newGoalRig(t)
	named, e := r.s.GoalList(r.ctx, asSession(r.root), contract.GoalListRequest{Session: "child"})
	if e != nil || len(named.Goals) != 1 || named.Goals[0].Session != r.child {
		t.Fatalf("by name %+v: %v", named, e)
	}
	if _, e := r.s.GoalList(r.ctx, asSession(r.root), contract.GoalListRequest{Session: "nobody"}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("unknown name: %v", e)
	}
}

func TestSessionListTakesAParentName(t *testing.T) {
	r := newGoalRig(t)
	listed, e := r.s.SessionList(r.ctx, asSession(r.root), contract.SessionListRequest{Room: r.room, Parent: "lead"})
	if e != nil || len(listed.Sessions) != 1 || listed.Sessions[0].ID != r.child {
		t.Fatalf("children of lead %+v: %v", listed, e)
	}
	if _, e := r.s.SessionList(r.ctx, asSession(r.root), contract.SessionListRequest{Room: r.room, Parent: "nobody"}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("unknown parent: %v", e)
	}
}

func TestGoalSetTakesASessionName(t *testing.T) {
	r := newGoalRig(t)
	set, e := r.s.GoalSet(r.ctx, asSession(r.root), contract.GoalSetRequest{Session: "lead", Text: "lead it", DoneWhen: "merged"})
	if e != nil {
		t.Fatal(e)
	}
	listed, e := r.s.GoalList(r.ctx, asSession(r.root), contract.GoalListRequest{Session: r.lead})
	if e != nil || len(listed.Goals) != 1 || listed.Goals[0].ID != set.Goal {
		t.Fatalf("lead's goals %+v: %v", listed, e)
	}
	if _, e := r.s.GoalSet(r.ctx, asSession(r.root), contract.GoalSetRequest{Session: "nobody", Text: "x", DoneWhen: "y"}); e == nil || e.Code != contract.ErrNotFound || !strings.Contains(e.Message, "nobody") {
		t.Fatalf("unknown name: %v", e)
	}
}
