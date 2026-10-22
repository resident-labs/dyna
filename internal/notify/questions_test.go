package notify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestQuestionDelivery(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	e.human(rec, "SLEEP:0 RESPOND: root ready", "@root")
	asker := e.wait(e.rootSession(rec).ID, 1)
	snd := Sender{Kind: SenderSession, Room: rec.Name, Session: asker}
	q, err := e.svc.Ask(e.ctx, snd, "", "", "May I download it?", "test a slice", []string{"yes", "no"}, []File{{Name: "card.html", Render: room.RenderHTML, Data: []byte("<html><title>Slice</title><body>preview</body></html>")}})
	if err != nil {
		t.Fatal(err)
	}
	if q.To != "owner" {
		t.Fatalf("target: %+v", q)
	}
	p, err := e.svc.Rooms.Message(rec.ID, q.Post)
	if err != nil || p.Kind != "message" || len(p.Attachments) != 1 || !p.Addressed(rec.Name) || !strings.Contains(p.Text, "dyna ask answer "+q.ID) {
		t.Fatal(p, err)
	}
	if err = e.svc.AnswerQuestion(e.ctx, Sender{Kind: SenderOwner, Room: rec.Name}, "", q.ID, "yes", "Keep the slice."); err != nil {
		t.Fatal(err)
	}
	e.wait(asker.ID, 2)
	msgs, _ := e.svc.Rooms.Messages(rec.ID)
	found := false
	for _, p := range msgs {
		if strings.HasPrefix(p.Text, "[ANSWER "+q.ID+"]") && p.Addressed(asker.Address()) {
			found = true
		}
	}
	if !found {
		t.Fatal("answer did not reach asker")
	}
	if err = e.svc.DeliverAnswers(e.ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	msgs2, _ := e.svc.Rooms.Messages(rec.ID)
	if len(msgs2) != len(msgs) {
		t.Fatal("duplicate delivery")
	}
	q, err = e.svc.Ask(e.ctx, snd, "", "", "Still needed?", "test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.mgr.CloseWithOptions(e.ctx, asker.ID, subagent.CloseOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	qs, _ := e.svc.QuestionStore().List(rec.ID)
	for _, x := range qs {
		if x.ID == q.ID && (x.State != "withdrawn" || x.Withdrawn.Reason != "session closed") {
			t.Fatal(x)
		}
	}
}
func TestWrapUpNeverReports(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	asker := e.start(rec, "asker", nil)
	turn, _ := e.mgr.Store.UpdateTurn(asker.ID, 1, func(turn *subagent.Turn) {
		turn.WrapUp = true
		turn.Status = subagent.TurnError
		turn.Error = "provider error"
	})
	result, err := e.svc.CompleteSessionTurn(e.ctx, asker.ID, turn.N)
	if err != nil || result.Message.ID != "" {
		t.Fatal(result, err)
	}
}

func TestGroupedQuestionAnswersReachEachAsker(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	first := e.start(rec, "first", nil)
	second := e.start(rec, "second", nil)
	for _, asker := range []subagent.Info{first, second} {
		if _, err := e.svc.QuestionStore().Ask(questions.Question{Room: rec.ID, Asker: asker.ID, To: "owner", Text: "Same question?", Meanwhile: "test", Options: []string{"yes", "no"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// An agent may answer its own question. The other asker still receives a
	// normal answer post, while the answerer needs no redundant self-wake.
	if err := e.svc.AnswerQuestion(e.ctx, Sender{Kind: SenderSession, Room: rec.Name, Session: first}, "", "q1", "yes", ""); err != nil {
		t.Fatal(err)
	}
	e.wait(second.ID, 2)
	msgs, _ := e.svc.Rooms.Messages(rec.ID)
	answers := 0
	for _, p := range msgs {
		if strings.HasPrefix(p.Text, "[ANSWER ") {
			answers++
			if p.From != first.Address() {
				t.Fatal(p.From)
			}
		}
	}
	if answers != 2 {
		t.Fatal(answers)
	}
	if err := e.svc.AnswerQuestion(e.ctx, Sender{Kind: SenderSession, Room: rec.Name, Session: first}, "", "q1", "yes", ""); err != nil {
		t.Fatal(err)
	}
	again, _ := e.svc.Rooms.Messages(rec.ID)
	if len(again) != len(msgs) {
		t.Fatal("retry duplicated answer")
	}
}

type questionUnavailableSpawner struct{}

func (questionUnavailableSpawner) Spawn(*subagent.Manager, string, int, *os.File) error {
	return errors.New("runner unavailable")
}

func TestSavedAnswerWaitsForEngineWithoutAnotherPost(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	e.human(rec, "SLEEP:0 RESPOND: root ready", "@root")
	asker := e.wait(e.rootSession(rec).ID, 1)
	q, err := e.svc.Ask(e.ctx, Sender{Kind: SenderSession, Room: rec.Name, Session: asker}, "", "", "Keep it?", "test", []string{"yes", "no"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spawner := e.mgr.Spawner
	e.mgr.Spawner = questionUnavailableSpawner{}
	owner := Sender{Kind: SenderOwner, Room: rec.Name}
	if err = e.svc.AnswerQuestion(e.ctx, owner, "", q.ID, "yes", ""); err != nil {
		t.Fatal(err)
	}
	qs, _ := e.svc.QuestionStore().List(rec.ID)
	if qs[0].State != "answered" || !qs[0].AnswerDelivered {
		t.Fatal(qs[0])
	}
	e.mgr.Spawner = spawner
	if err = e.svc.AnswerQuestion(e.ctx, owner, "", q.ID, "yes", ""); err != nil {
		t.Fatal(err)
	}
	e.wait(asker.ID, 2)
	msgs, _ := e.svc.Rooms.Messages(rec.ID)
	answers := 0
	for _, p := range msgs {
		if strings.HasPrefix(p.Text, "[ANSWER ") {
			answers++
		}
	}
	if answers != 1 {
		t.Fatal(answers)
	}
	qs, _ = e.svc.QuestionStore().List(rec.ID)
	if !qs[0].AnswerDelivered {
		t.Fatal(qs[0])
	}
}

func TestQuestionWithdrawalKeepsActorAndParticipants(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	e.human(rec, "SLEEP:0 RESPOND: root ready", "@root")
	asker := e.wait(e.rootSession(rec).ID, 1)
	target := e.start(rec, "target", nil)
	q, err := e.svc.Ask(e.ctx, Sender{Kind: SenderSession, Room: rec.Name, Session: asker}, "", "", "Still needed?", "test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.WithdrawQuestion(Sender{Kind: SenderSession, Room: rec.Name, Session: target}, "", q.ID, "obsolete"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := e.svc.Rooms.Messages(rec.ID)
	p := msgs[len(msgs)-1]
	if p.From != target.Address() || !p.Addressed(asker.Address()) || p.Addressed(target.Address()) || !p.Addressed(rec.Name) || p.Kind != "message" || p.ReplyTo != q.Post {
		t.Fatal(p)
	}
}

// Agents can answer posts, so a question to an agent is a post. Questions
// wait only for the owner, and an agent target names the right command.
func TestAskRejectsAgentTargets(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	e.start(rec, "target", nil)
	snd := Sender{Kind: SenderOwner, Room: rec.Name}
	for _, to := range []string{"@target", "@parent", "target"} {
		if _, err := e.svc.Ask(e.ctx, snd, "", to, "Which fix?", "test", nil, nil); !errors.Is(err, ErrAskAgent) || err.Error() != "Ask agents with dyna post; dyna ask is for the owner." {
			t.Fatalf("%s: %v", to, err)
		}
	}
	if qs, _ := e.svc.QuestionStore().List(rec.ID); len(qs) != 0 {
		t.Fatalf("rejected questions were stored: %+v", qs)
	}
	q, err := e.svc.Ask(e.ctx, snd, "", "@root", "Which fix?", "test", nil, nil)
	if err != nil || q.To != "owner" {
		t.Fatalf("owner question: %+v %v", q, err)
	}
}

// The human owner only answers. Root asks and withdraws from the CLI.
func TestHumanOwnerOnlyAnswersQuestions(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	human := Sender{Kind: SenderOwner, Room: rec.Name, Human: true}
	if _, err := e.svc.Ask(e.ctx, human, "", "", "Ship it?", "wait", nil, nil); err == nil || !strings.Contains(err.Error(), "is root only;") {
		t.Fatalf("human ask: %v", err)
	}
	root := Sender{Kind: SenderOwner, Room: rec.Name}
	q, err := e.svc.Ask(e.ctx, root, "", "", "Ship it?", "wait", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.WithdrawQuestion(human, "", q.ID, "no"); err == nil || !strings.Contains(err.Error(), "ask withdraw is root or session or worker only;") {
		t.Fatalf("human withdraw: %v", err)
	}
	if err = e.svc.AnswerQuestion(e.ctx, human, "", q.ID, "", "yes"); err != nil {
		t.Fatalf("human answer: %v", err)
	}
}

func TestAskRejectsNonRootSendersBeforeSaving(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	child := e.start(rec, "child", nil)
	for _, snd := range []Sender{
		{Kind: SenderSession, Room: rec.Name, Session: child},
		{Kind: SenderWorker, Room: rec.Name, RunID: "run-fixture", Agent: 1},
		{Kind: SenderScript, Room: rec.Name, RunID: "run-fixture"},
	} {
		q, err := e.svc.Ask(e.ctx, snd, "", "", "Can I continue?", "inspect code", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "dyna post @parent") || q.ID != "" {
			t.Fatalf("non-root ask = %+v, %v", q, err)
		}
	}
	qs, err := e.svc.QuestionStore().List(rec.ID)
	if err != nil || len(qs) != 0 {
		t.Fatalf("rejected asks saved questions: %+v %v", qs, err)
	}
}

func TestQuestionStoreIsStableForItsDataRoot(t *testing.T) {
	e := newTestEnv(t)
	first := e.svc.QuestionStore()
	if second := e.svc.QuestionStore(); first != second {
		t.Fatal("notify recreated its questions store")
	}
	e.svc.QuestionRoot = filepath.Join(t.TempDir(), "questions")
	changed := e.svc.QuestionStore()
	if changed == first || changed.Root != e.svc.QuestionRoot {
		t.Fatal("questions store did not follow the configured data root")
	}
	if e.svc.QuestionStore() != changed {
		t.Fatal("configured questions store was recreated")
	}
}

// Posting a question records it on the asker's current turn, which takes
// the asker's session lock. Ask must not hold that lock, or the database
// writer, when it posts.
func TestRootAskDuringItsTurnPostsWithoutDeadlock(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	e.human(rec, "SLEEP:0 RESPOND: root ready", "@root")
	asker := e.wait(e.rootSession(rec).ID, 1)
	asker.CurrentTurn = asker.Turns
	done := make(chan error, 1)
	go func() {
		_, err := e.svc.Ask(e.ctx, Sender{Kind: SenderSession, Room: rec.Name, Session: asker}, "", "", "Keep it?", "test", nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask deadlocked while posting during the asker's turn")
	}
	turn, err := e.mgr.Store.ReadTurn(asker.ID, asker.Turns)
	if err != nil || len(turn.PostedTo) == 0 {
		t.Fatalf("turn after Ask = %+v, %v; want the question post recorded", turn.PostedTo, err)
	}
}

func TestUnverifiedQuestionRecordIdentity(t *testing.T) {
	snd := Sender{Kind: SenderUnverified}
	if got := questionIdentity(snd); got != "unverified" {
		t.Fatalf("question record identity = %q; want unverified", got)
	}
	if got := questionSenderAddress(snd, "room"); got != "unverified" {
		t.Fatalf("withdrawal post author = %q; want unverified", got)
	}
}
