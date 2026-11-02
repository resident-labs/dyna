package engine

import (
	"context"
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
