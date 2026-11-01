package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var questionOwner = contract.Principal{Kind: contract.PrincipalOwner}

func asSession(id contract.SessionID) contract.Principal {
	return contract.Principal{Kind: contract.PrincipalSession, Session: id}
}

// grantAsk gives a non-root session the ask-owner grant.
func grantAsk(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, id contract.SessionID) {
	t.Helper()
	g := contract.Grant{ID: contract.GrantID("grant_test_" + string(id)), Kind: contract.GrantAskOwner, Room: room, To: asSession(id), By: questionOwner, Created: s.now()}
	if err := s.store.CreateGrant(ctx, g); err != nil {
		t.Fatal(err)
	}
}

func ask(t *testing.T, s *Service, ctx context.Context, id contract.SessionID, text string) contract.Question {
	t.Helper()
	asked, problem := s.QuestionAsk(ctx, asSession(id), contract.QuestionAskRequest{Text: text, Meanwhile: "the next item"})
	if problem != nil {
		t.Fatal(problem)
	}
	return asked.Question
}

func answer(t *testing.T, s *Service, ctx context.Context, id contract.QuestionID, text string) {
	t.Helper()
	if _, problem := s.QuestionAnswer(ctx, questionOwner, contract.QuestionAnswerRequest{ID: id, Text: text}); problem != nil {
		t.Fatal(problem)
	}
}

func pendingQuestions(t *testing.T, s *Service, ctx context.Context, room contract.RoomID) []contract.Question {
	t.Helper()
	list, problem := s.QuestionList(ctx, questionOwner, contract.QuestionListRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	var pending []contract.Question
	for _, q := range list.Questions {
		if q.State == contract.QuestionStatePending {
			pending = append(pending, q)
		}
	}
	return pending
}

func TestRootAsksAndTheAnswerWakesIt(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	q := ask(t, s, ctx, root, "  Which database?  ")
	if q.Text != "Which database?" || q.State != contract.QuestionStatePending || q.Room != room {
		t.Fatalf("question: %+v", q)
	}
	rooms, problem := s.RoomList(ctx, questionOwner, contract.RoomListRequest{})
	if problem != nil || len(rooms.Rooms) != 1 || len(rooms.Rooms[0].Questions) != 1 || rooms.Rooms[0].Questions[0] != q.ID {
		t.Fatalf("Needs you: %+v %v", rooms, problem)
	}
	// A session reads the same list for its own room without naming it.
	own, problem := s.QuestionList(ctx, asSession(root), contract.QuestionListRequest{})
	if problem != nil || len(own.Questions) != 1 || own.Questions[0].ID != q.ID {
		t.Fatalf("own list: %+v %v", own, problem)
	}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: root}
	page, problem := s.EventsList(ctx, questionOwner, contract.EventsListRequest{Room: room, Home: &dm})
	if problem != nil || len(page.Events) != 1 {
		t.Fatalf("DM events: %+v %v", page, problem)
	}
	if asked, ok := page.Events[0].Body.(contract.QuestionAsked); !ok || asked.ID != q.ID {
		t.Fatalf("DM event: %+v", page.Events[0])
	}

	answer(t, s, ctx, q.ID, "Postgres")
	start := nextTurn(t, s, ctx, root)
	if len(start.Inputs) != 1 {
		t.Fatalf("inputs: %+v", start.Inputs)
	}
	input := start.Inputs[0]
	if !strings.Contains(input.Text, "Postgres") || !strings.Contains(input.Text, "Which database?") || !strings.Contains(input.Text, "From the owner, in your DM") {
		t.Fatalf("input: %q", input.Text)
	}
	// The reply goes back where the owner answered.
	if input.Surface != contract.SurfaceDM || input.Reply.Kind != contract.ReplyFinal || input.Reply.Target.Kind != contract.PrincipalOwner || input.Reply.Home != dm {
		t.Fatalf("channel: %+v", input)
	}
	if got := pendingQuestions(t, s, ctx, room); len(got) != 0 {
		t.Fatalf("still waiting: %+v", got)
	}
	if _, problem := s.QuestionAnswer(ctx, questionOwner, contract.QuestionAnswerRequest{ID: q.ID, Text: "again"}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("second answer: %v", problem)
	}
	// The DM thread reads the answer from the turn's inputs.
	thread, problem := s.TranscriptRead(ctx, questionOwner, contract.TranscriptReadRequest{Session: root})
	if problem != nil || len(thread.Turns) != 1 || len(thread.Turns[0].Inputs) != 1 {
		t.Fatalf("thread: %+v %v", thread, problem)
	}
	if got := thread.Turns[0].Inputs[0]; got.Text != "Question: Which database?\n\nAnswer: Postgres" || got.From.Kind != contract.PrincipalOwner || got.Home == nil || *got.Home != dm {
		t.Fatalf("thread input: %+v", got)
	}
}

func TestAskNeedsTheGrant(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if _, problem := s.QuestionAsk(ctx, asSession(worker), contract.QuestionAskRequest{Text: "may I?", Meanwhile: "the next item"}); problem == nil || problem.Code != contract.ErrNoGrant {
		t.Fatalf("no grant: %v", problem)
	}
	if got := pendingQuestions(t, s, ctx, room); len(got) != 0 {
		t.Fatalf("refused question stored: %+v", got)
	}
	grantAsk(t, s, ctx, room, worker)
	ask(t, s, ctx, worker, "may I?")
	if _, problem := s.QuestionAsk(ctx, questionOwner, contract.QuestionAskRequest{Text: "owner", Meanwhile: "the next item"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("owner asked: %v", problem)
	}
	if _, problem := s.QuestionAnswer(ctx, asSession(worker), contract.QuestionAnswerRequest{ID: "q_x", Text: "self"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session answered: %v", problem)
	}
}

func TestAskRefusesEmptyAndOversizedText(t *testing.T) {
	s, _, root, ctx := setupRoot(t)
	for _, text := range []string{"  ", strings.Repeat("x", maxQuestionChars+1)} {
		if _, problem := s.QuestionAsk(ctx, asSession(root), contract.QuestionAskRequest{Text: text, Meanwhile: "the next item"}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Fatalf("text of %d: %v", len(text), problem)
		}
	}
}

func TestAskRefusesABlankOrOversizedMeanwhile(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	for _, blocked := range []bool{false, true} {
		for _, meanwhile := range []string{"", " \n\t ", strings.Repeat("x", maxQuestionChars+1)} {
			_, problem := s.QuestionAsk(ctx, asSession(root), contract.QuestionAskRequest{Text: "Which?", Meanwhile: meanwhile, Blocked: blocked})
			if problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(problem.Message, "meanwhile") {
				t.Fatalf("meanwhile of %d, blocked %v: %v", len(meanwhile), blocked, problem)
			}
		}
	}
	if got := pendingQuestions(t, s, ctx, room); len(got) != 0 {
		t.Fatalf("refused question stored: %+v", got)
	}
}

func TestAskKeepsMeanwhileAndBlocked(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	asked, problem := s.QuestionAsk(ctx, asSession(root), contract.QuestionAskRequest{Text: "Which key?", Meanwhile: "  every step needs the key  ", Blocked: true})
	if problem != nil {
		t.Fatal(problem)
	}
	if q := asked.Question; q.Meanwhile != "every step needs the key" || !q.Blocked {
		t.Fatalf("question: %+v", q)
	}
	got := pendingQuestions(t, s, ctx, room)
	if len(got) != 1 || got[0].Meanwhile != "every step needs the key" || !got[0].Blocked {
		t.Fatalf("listed: %+v", got)
	}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: root}
	page, problem := s.EventsList(ctx, questionOwner, contract.EventsListRequest{Room: room, Home: &dm})
	if problem != nil || len(page.Events) != 1 {
		t.Fatalf("DM events: %+v %v", page, problem)
	}
	if body, ok := page.Events[0].Body.(contract.QuestionAsked); !ok || body.Meanwhile != "every step needs the key" || !body.Blocked {
		t.Fatalf("event: %+v", page.Events[0])
	}
}

func TestAskRetryReturnsTheFirstQuestion(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	first, problem := s.QuestionAsk(ctx, asSession(root), contract.QuestionAskRequest{Text: "once", Meanwhile: "the next item", ClientID: "key"})
	if problem != nil {
		t.Fatal(problem)
	}
	second, problem := s.QuestionAsk(ctx, asSession(root), contract.QuestionAskRequest{Text: "once", Meanwhile: "the next item", ClientID: "key"})
	if problem != nil || second.Question.ID != first.Question.ID {
		t.Fatalf("retry: %+v %v", second, problem)
	}
	if got := pendingQuestions(t, s, ctx, room); len(got) != 1 {
		t.Fatalf("questions: %+v", got)
	}
}

func TestQuestionSurvivesAnEngineRestart(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Unix(1000, 0) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: now()}); err != nil {
		t.Fatal(err)
	}
	before := New(st, fakeBoxes{}, fakeProfiles{}, now)
	resetLifecycle(before)
	created, problem := before.RoomCreate(ctx, questionOwner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, before, ctx)
	var root contract.SessionID
	if err := st.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE parent IS NULL").Scan(&root); err != nil {
		t.Fatal(err)
	}
	q := ask(t, before, ctx, root, "still there?")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after := New(st, fakeBoxes{}, fakeProfiles{}, now)
	resetLifecycle(after)
	got := pendingQuestions(t, after, ctx, created.Room)
	if len(got) != 1 || got[0].ID != q.ID || got[0].Text != "still there?" || got[0].Meanwhile != "the next item" {
		t.Fatalf("after restart: %+v", got)
	}
	answer(t, after, ctx, q.ID, "yes")
	if start := nextTurn(t, after, ctx, root); !strings.Contains(start.Inputs[0].Text, "yes") {
		t.Fatalf("input: %+v", start.Inputs)
	}
}

func TestClosingASessionWithdrawsItsQuestion(t *testing.T) {
	s, room, worker, ctx := setup(t)
	child, problem := s.SessionStart(ctx, questionOwner, contract.SessionStartRequest{Room: room, Parent: worker, Name: "child", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	grantAsk(t, s, ctx, room, worker)
	grantAsk(t, s, ctx, room, child.Session)
	own, below := ask(t, s, ctx, worker, "mine"), ask(t, s, ctx, child.Session, "below")
	if _, problem := s.SessionClose(ctx, questionOwner, contract.SessionCloseRequest{Session: worker}); problem != nil {
		t.Fatal(problem)
	}
	// It goes when the close starts, since an archive can take a long time and nothing can then use an answer.
	if got := pendingQuestions(t, s, ctx, room); len(got) != 0 {
		t.Fatalf("still waiting: %+v", got)
	}
	rooms, problem := s.RoomList(ctx, questionOwner, contract.RoomListRequest{})
	if problem != nil || len(rooms.Rooms[0].Questions) != 0 {
		t.Fatalf("Needs you: %+v %v", rooms, problem)
	}
	events, problem := s.EventsList(ctx, questionOwner, contract.EventsListRequest{Room: room, Types: []contract.EventType{"question.withdrawn"}})
	if problem != nil || len(events.Events) != 2 {
		t.Fatalf("withdrawn events: %+v %v", events, problem)
	}
	for _, q := range []contract.Question{own, below} {
		if _, problem := s.QuestionAnswer(ctx, questionOwner, contract.QuestionAnswerRequest{ID: q.ID, Text: "late"}); problem == nil || problem.Code != contract.ErrConflict {
			t.Fatalf("answer after close: %v", problem)
		}
	}
	if _, problem := s.QuestionAsk(ctx, asSession(worker), contract.QuestionAskRequest{Text: "more", Meanwhile: "the next item"}); problem == nil || problem.Code != contract.ErrSessionClosing {
		t.Fatalf("ask while closing: %v", problem)
	}
}

// stopForGood makes the session a leaf that the dormancy sweep may close.
func stopForGood(t *testing.T, s *Service, ctx context.Context, id contract.SessionID) {
	t.Helper()
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_id=?", id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='stopped',want='stop',updated_at=0 WHERE session=?", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func sessionState(t *testing.T, s *Service, ctx context.Context, id contract.SessionID) string {
	t.Helper()
	var state string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAWaitingQuestionKeepsADormantSessionOpen(t *testing.T) {
	s, room, worker, ctx := setup(t)
	if _, problem := s.GoalDone(ctx, questionOwner, contract.GoalDoneRequest{Room: room, Goal: "g1", Summary: "finished"}); problem != nil {
		t.Fatal(problem) // An open goal would keep it open on its own.
	}
	grantAsk(t, s, ctx, room, worker)
	q := ask(t, s, ctx, worker, "before you go?")
	stopForGood(t, s, ctx, worker)
	s.now = func() time.Time { return time.Unix(1000000, 0) }
	if err := s.CloseDormant(ctx); err != nil {
		t.Fatal(err)
	}
	if state := sessionState(t, s, ctx, worker); state != "open" {
		t.Fatalf("closed while waiting: %s", state)
	}
	answer(t, s, ctx, q.ID, "go ahead")
	stopForGood(t, s, ctx, worker)
	if err := s.CloseDormant(ctx); err != nil {
		t.Fatal(err)
	}
	if state := sessionState(t, s, ctx, worker); state != "closing" {
		t.Fatalf("answered session stayed: %s", state)
	}
}

func TestAWaitingQuestionKeepsASessionFromDiskEviction(t *testing.T) {
	s, room, worker, ctx := setup(t)
	grantAsk(t, s, ctx, room, worker)
	ask(t, s, ctx, worker, "before you go?")
	stopForGood(t, s, ctx, worker)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET host='host' WHERE session=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	holdLow(t, s, ctx, 5)
	if err := s.EvictDisk(ctx); err != nil {
		t.Fatal(err)
	}
	if state := sessionState(t, s, ctx, worker); state != "open" {
		t.Fatalf("evicted while waiting: %s", state)
	}
}

// A question does not keep a box running; the answer starts it again.
func TestAnAnswerWakesAStoppedBox(t *testing.T) {
	s, h, room, sid, ctx := quietSession(t, 0)
	grantAsk(t, s, ctx, room, sid)
	q := ask(t, s, ctx, sid, "while you sleep?")
	passLifecycle(t, s, ctx)
	if h.stops != 1 || stateOf(t, s, ctx, sid) != contract.BoxStopped {
		t.Fatal("quiet box did not stop")
	}
	answer(t, s, ctx, q.ID, "wake up")
	passLifecycle(t, s, ctx)
	if h.starts != 1 || stateOf(t, s, ctx, sid) != contract.BoxStarting {
		t.Fatal("answer did not start the box")
	}
}

func TestSnapshotShowsAQuestionToItsAskerAndParent(t *testing.T) {
	s, room, worker, ctx := setup(t)
	grantAsk(t, s, ctx, room, worker)
	q := ask(t, s, ctx, worker, "Which\ndatabase "+strings.Repeat("x", 200))
	snapshot, problem := s.ContextSnapshot(ctx, asSession(worker), contract.ContextSnapshotRequest{Section: contract.ContextRequests})
	if problem != nil || len(snapshot.Sections) != 1 {
		t.Fatalf("snapshot: %+v %v", snapshot, problem)
	}
	line := snapshot.Sections[0].Text
	if !strings.HasPrefix(line, string(q.ID)+": question, waiting: Which database x") || strings.Contains(line, "\n") || len([]rune(line)) > len(q.ID)+len(": question, waiting: ")+requestChars {
		t.Fatalf("line: %q", line)
	}
	var root contract.SessionID
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker).Scan(&root); err != nil {
		t.Fatal(err)
	}
	snapshot, problem = s.ContextSnapshot(ctx, asSession(root), contract.ContextSnapshotRequest{Section: contract.ContextChildren})
	if problem != nil || len(snapshot.Sections) != 1 || !strings.Contains(snapshot.Sections[0].Text, "waiting for the owner's answer") {
		t.Fatalf("parent: %+v %v", snapshot, problem)
	}
	answer(t, s, ctx, q.ID, "this one")
	snapshot, problem = s.ContextSnapshot(ctx, asSession(root), contract.ContextSnapshotRequest{Section: contract.ContextChildren})
	if problem != nil || strings.Contains(snapshot.Sections[0].Text, "waiting for the owner's answer") {
		t.Fatalf("parent after the answer: %+v %v", snapshot, problem)
	}
}
