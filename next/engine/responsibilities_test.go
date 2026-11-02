package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

func (r *goalRig) ownsPR(session contract.SessionID, number int) {
	r.t.Helper()
	r.exec("INSERT OR IGNORE INTO repos(repo,default_room) VALUES('o/r',?)", r.room)
	r.exec("INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('o/r',?,?,'','')", number, session)
}

func (r *goalRig) ownsIssue(session contract.SessionID, number int) {
	r.t.Helper()
	r.exec("INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES('o/r',?,?,'u','t','a','open')", number, session)
}

// closes ends a session the way the reconciler does once its box is gone.
func (r *goalRig) closes(session contract.SessionID) {
	r.t.Helper()
	event, err := r.s.event(r.room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionClosed{Session: session})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.st.CloseSession(r.ctx, session, event); err != nil {
		r.t.Fatal(err)
	}
}

// closedNotice reads the session.closed delivered to a session, or nothing.
func (r *goalRig) closedNotice(to contract.SessionID) (contract.SessionClosed, string, bool) {
	r.t.Helper()
	var seq int64
	err := r.st.Read().QueryRowContext(r.ctx, "SELECT d.seq FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_kind='session' AND d.to_id=? AND e.type='session.closed' ORDER BY d.seq DESC LIMIT 1", to).Scan(&seq)
	if err != nil {
		return contract.SessionClosed{}, "", false
	}
	event, err := r.st.Event(r.ctx, seq)
	if err != nil {
		r.t.Fatal(err)
	}
	text, err := room.RenderDelivery(event.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return event.Body.(contract.SessionClosed), text, true
}

func TestCloseHandsOverWhatTheListOwesAndNamesIt(t *testing.T) {
	r := newGoalRig(t)
	r.ownsPR(r.child, 7)
	r.ownsIssue(r.child, 8)
	r.askAs(r.child, false)
	r.exec("UPDATE sessions SET shell_jobs=1 WHERE id=?", r.child)
	r.notice(r.child)
	r.begin(r.child)
	closing, e := r.s.SessionClose(r.ctx, ownerCaller, contract.SessionCloseRequest{Session: r.child})
	if e != nil {
		t.Fatalf("close refused: %v", e)
	}
	names := func(items []contract.Responsibility) string { return room.ResponsibilityNames(items) }
	if closing.To != r.lead || names(closing.Handed) != "goal g1, PR o/r#7, issue o/r#8" {
		t.Fatalf("handed %+v", closing)
	}
	if !strings.Contains(names(closing.Ended), "shell job") || !strings.Contains(names(closing.Ended), "question ") {
		t.Fatalf("ended %+v", closing.Ended)
	}
	r.closes(r.child)
	notice, text, found := r.closedNotice(r.lead)
	if !found || len(notice.Unheld) != 0 || !strings.HasSuffix(text, " closed and handed you goal g1, PR o/r#7, issue o/r#8.") {
		t.Fatalf("notice %+v %q", notice, text)
	}
	held, err := r.st.Responsibilities(r.ctx, r.lead)
	if err != nil || held.Count(contract.ResponsibilityGoal) != 1 || held.Count(contract.ResponsibilityPR) != 1 || held.Count(contract.ResponsibilityIssue) != 1 || held.Count(contract.ResponsibilityChild) != 0 {
		t.Fatalf("the parent holds %+v: %v", held, err)
	}
}

func TestCloseUnderRootLeavesGoalsUnheldAndHandsTheRest(t *testing.T) {
	r := newGoalRig(t)
	r.ownsPR(r.lead, 7)
	r.exec("UPDATE goals SET session=? WHERE number=1", r.lead)
	if _, e := r.s.SessionClose(r.ctx, asSession(r.root), contract.SessionCloseRequest{Session: r.child}); e != nil {
		t.Fatal(e)
	}
	r.closes(r.child)
	if _, _, found := r.closedNotice(r.lead); found {
		t.Fatal("a child with nothing to hand over wrote a notice")
	}
	r.closes(r.lead)
	notice, text, found := r.closedNotice(r.root)
	if !found || len(notice.Unheld) != 1 || len(notice.Handed) != 1 || !strings.Contains(text, "handed you PR o/r#7 and left goals with no holder: g1") {
		t.Fatalf("notice %+v %q", notice, text)
	}
}

func TestAClosingParentGetsNoNoticeOfItsOwnSubtree(t *testing.T) {
	r := newGoalRig(t)
	plan, e := r.s.SessionClose(r.ctx, ownerCaller, contract.SessionCloseRequest{Session: r.lead})
	if e != nil {
		t.Fatal(e)
	}
	if plan.To != r.root || len(plan.Handed) != 1 || plan.Handed[0].ID != "g1" {
		t.Fatalf("a subtree's plan %+v", plan)
	}
	r.closes(r.child)
	if _, _, found := r.closedNotice(r.lead); found {
		t.Fatal("the closing lead was told")
	}
	r.closes(r.lead)
	if notice, _, found := r.closedNotice(r.root); !found || len(notice.Unheld) != 1 {
		t.Fatalf("root's notice %+v", notice)
	}
}

func TestOwnerAndAncestorsCloseWhateverIsHeldButNobodyElseDoes(t *testing.T) {
	r := newGoalRig(t)
	r.ownsPR(r.child, 7)
	r.askAs(r.child, true)
	r.notice(r.child)
	r.begin(r.child)
	if _, e := r.s.SessionClose(r.ctx, asSession(r.child), contract.SessionCloseRequest{Session: r.child}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("a session closed itself: %v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, asSession(r.lead), contract.SessionCloseRequest{Session: r.child}); e != nil {
		t.Fatalf("the parent was refused: %v", e)
	}
	if _, e := r.s.SessionClose(r.ctx, ownerCaller, contract.SessionCloseRequest{Session: r.root}); e == nil {
		t.Fatal("root closed")
	}
}

func TestSessionResponsibilitiesReadsTheListAndTheClosePlan(t *testing.T) {
	r := newGoalRig(t)
	r.ownsPR(r.child, 7)
	read, e := r.s.SessionResponsibilities(r.ctx, ownerCaller, contract.SessionResponsibilitiesRequest{Session: r.child})
	if e != nil {
		t.Fatal(e)
	}
	if room.ResponsibilityNames(read.Items) != "goal g1, PR o/r#7" || read.Close.To != r.lead || len(read.Close.Handed) != 2 {
		t.Fatalf("read %+v", read)
	}
	read, e = r.s.SessionResponsibilities(r.ctx, ownerCaller, contract.SessionResponsibilitiesRequest{Session: r.root})
	if e != nil || read.Close.To != "" || len(read.Close.Handed) != 0 || read.Items == nil {
		t.Fatalf("root %+v: %v", read, e)
	}
}

func TestTheAgentsSectionListsWhatIsOpenAndWakesNobody(t *testing.T) {
	r := newGoalRig(t)
	turn := r.begin(r.child)
	if _, e := r.s.TurnOutput(r.ctx, asSession(r.child), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "ok", ShellJobs: 1, Jobs: []contract.ShellJobID{"job_7f3b"}}); e != nil {
		t.Fatal(e)
	}
	r.ownsPR(r.child, 6216)
	r.ownsIssue(r.child, 80)
	var before int
	count := "SELECT count(*) FROM deliveries"
	r.notice(r.child)
	if err := r.st.Read().QueryRowContext(r.ctx, count).Scan(&before); err != nil {
		t.Fatal(err)
	}
	next := r.begin(r.child)
	snapshot, e := r.s.ContextSnapshot(r.ctx, asSession(r.child), contract.ContextSnapshotRequest{})
	if e != nil {
		t.Fatal(e)
	}
	var keys []contract.ContextSectionKey
	text := ""
	for _, section := range snapshot.Sections {
		keys = append(keys, section.Key)
		if section.Key == contract.ContextHolds {
			text = section.Text
		}
	}
	want := "You still hold: shell job job_7f3b, goal g1, PR o/r#6216, issue o/r#80.\nPR o/r#6216: CI not read yet, not approved"
	if text != want {
		t.Fatalf("section %q", text)
	}
	if strings.Contains(text, string(next)) {
		t.Fatal("the section names the turn being dispatched")
	}
	if !reflect.DeepEqual(keys, []contract.ContextSectionKey{contract.ContextGoal, contract.ContextHolds}) {
		t.Fatalf("sections %v", keys)
	}
	var after int
	if err := r.st.Read().QueryRowContext(r.ctx, count).Scan(&after); err != nil || after != before {
		t.Fatalf("deliveries went from %d to %d: %v", before, after, err)
	}
	r.exec("UPDATE goals SET state='done'")
	r.exec("UPDATE pr_owners SET state='merged'")
	r.exec("UPDATE issue_owners SET state='closed'")
	r.exec("UPDATE sessions SET shell_jobs=0 WHERE id=?", r.child)
	snapshot, e = r.s.ContextSnapshot(r.ctx, asSession(r.child), contract.ContextSnapshotRequest{})
	if e != nil {
		t.Fatal(e)
	}
	for _, section := range snapshot.Sections {
		if section.Key == contract.ContextHolds {
			t.Fatalf("nothing is open, yet %q", section.Text)
		}
	}
}

func TestALongListStillFitsItsFirstLine(t *testing.T) {
	r := newGoalRig(t)
	for number := range 200 {
		r.ownsIssue(r.child, number+1)
	}
	snapshot, e := r.s.ContextSnapshot(r.ctx, asSession(r.child), contract.ContextSnapshotRequest{})
	if e != nil {
		t.Fatal(e)
	}
	for _, section := range snapshot.Sections {
		if section.Key == contract.ContextHolds && (!strings.HasPrefix(section.Text, "You still hold: goal g1, issue o/r#1,") || !strings.Contains(section.Text, "more: context show") || len(section.Text) > holdsChars) {
			t.Fatalf("section %q", section.Text)
		}
	}
	full, e := r.s.ContextSnapshot(r.ctx, asSession(r.child), contract.ContextSnapshotRequest{Section: contract.ContextHolds})
	if e != nil || len(full.Sections) != 1 || !strings.Contains(full.Sections[0].Text, "issue o/r#200.") {
		t.Fatalf("full %+v: %v", full, e)
	}
}

func TestAJobOnTheListHoldsOffACheckinForAsLongAsItRuns(t *testing.T) {
	r := newGoalRig(t)
	turn := r.begin(r.child)
	if _, e := r.s.TurnOutput(r.ctx, asSession(r.child), contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Final: "building", ShellJobs: 1, Jobs: []contract.ShellJobID{"job_1"}}); e != nil {
		t.Fatal(e)
	}
	r.turn(r.lead, "good")
	r.advance(room.CheckinStuck + time.Hour)
	r.pass()
	r.wantCheckins(r.lead, 0)
	if _, e := r.s.ShellJobEnded(r.ctx, asSession(r.child), contract.ShellJobEndedRequest{Job: "job_1"}); e != nil {
		t.Fatal(e)
	}
	r.turn(r.child, "")
	r.advance(room.CheckinIdle + time.Minute)
	r.pass()
	r.wantCheckins(r.lead, 1)
}

func TestANewBoxAgentForgetsTheJobsOfTheLastOne(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 1)
	held, err := s.store.Responsibilities(ctx, sid)
	if err != nil || !held.NeedsBox() {
		t.Fatalf("held %+v: %v", held, err)
	}
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	if held, err = s.store.Responsibilities(ctx, sid); err != nil || held.NeedsBox() {
		t.Fatalf("held %+v: %v", held, err)
	}
}
