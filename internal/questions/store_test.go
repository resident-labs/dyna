package questions

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func sample(asker string) Question {
	return Question{Room: "room", Asker: asker, AskerKind: AskerSession, Text: "May I download it?", Meanwhile: "test a slice", Options: []string{"yes", "no"}}
}

// holdWriter stores q as the room's next open question inside a store
// transaction that stays open until release, so tests can read during it.
func holdWriter(s *Store, q Question, entered, release chan struct{}) error {
	return s.update(func(st *State) error {
		q.ID = fmt.Sprintf("q%d", st.Next[q.Room]+1)
		q.State, q.To, q.AskedAt = "open", "owner", time.Now()
		st.Next[q.Room]++
		st.Questions[key(q.Room, q.ID)] = q
		close(entered)
		<-release
		return nil
	})
}

func TestStore(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	q, err := s.Ask(sample("a"), func(q Question) (string, error) { return "post_" + q.ID, nil })
	if err != nil || q.ID != "q1" || q.Post != "post_q1" {
		t.Fatalf("ask: %+v %v", q, err)
	}
	other := sample("b")
	other.Text = "  MAY I   download it?  "
	q2, err := s.Ask(other, nil)
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.Answer("room", q.ID, Answer{By: "owner", Choice: "yes", Text: "Keep the slice."})
	if err != nil || len(group) != 2 {
		t.Fatalf("group: %+v %v", group, err)
	}
	if _, err = s.Answer("room", q2.ID, Answer{Choice: "no"}); err == nil {
		t.Fatal("answered twice")
	}
	q3, err := s.Ask(sample("c"), nil)
	if err != nil || q3.ID != "q3" {
		t.Fatal(q3, err)
	}
	q3, err = s.Withdraw("room", q3.ID, "c", false, "slice was enough")
	if err != nil || q3.Withdrawn.Reason != "slice was enough" {
		t.Fatal(q3, err)
	}
	read := &Store{Root: s.Root}
	qs, err := read.List("room")
	if err != nil || len(qs) != 3 {
		t.Fatal(qs, err)
	}
	q4, _ := s.Ask(sample("d"), nil)
	closed, err := s.CloseSession("room", "d")
	if err != nil || len(closed) != 1 || closed[0].ID != q4.ID || closed[0].Withdrawn.Reason != "session closed" {
		t.Fatal(closed, err)
	}
}
func TestValidation(t *testing.T) {
	for _, change := range []func(*Question){func(q *Question) { q.Meanwhile = " " }, func(q *Question) { q.Text = strings.Repeat("é", 2001) }, func(q *Question) { q.Options = []string{""} }, func(q *Question) { q.Options = []string{"yes", "yes"} }} {
		q := sample("a")
		change(&q)
		if err := Validate(q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	s := &Store{Root: questionRoot(t)}
	q, _ := s.Ask(sample("a"), nil)
	if _, err := s.Answer("room", q.ID, Answer{Choice: "maybe"}); err == nil {
		t.Fatal("invalid choice")
	}
}

func TestGroupedAnswersStayWithRecipient(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	agent := sample("asker")
	agent.To = "target"
	q, err := s.Ask(agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Ask(sample("another"), nil)
	if err != nil {
		t.Fatal(err)
	}
	answered, err := s.Answer("room", q.ID, Answer{By: "target", Text: "Small fix."})
	if err != nil || len(answered) != 1 || answered[0].ID != q.ID {
		t.Fatalf("answers: %+v, %v", answered, err)
	}
	list, err := s.List("room")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.ID == owner.ID && item.State != "open" {
			t.Fatal("agent answer resolved owner question")
		}
	}
}

// A rejected post stores no question, and its ID is never given out again.
func TestRejectedPostStoresNoQuestion(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	if _, err := s.Ask(sample("a"), func(Question) (string, error) { return "", errors.New("post failed") }); err == nil {
		t.Fatal("Ask passed a rejected post")
	}
	if qs, err := s.List("room"); err != nil || len(qs) != 0 {
		t.Fatalf("questions after a rejected post = %+v, %v", qs, err)
	}
	q, err := s.Ask(sample("a"), func(Question) (string, error) { return "post_1", nil })
	if err != nil || q.ID != "q2" || q.Post != "post_1" {
		t.Fatalf("next question = %+v, %v; want q2 with its post", q, err)
	}
	if qs, _ := s.List("room"); len(qs) != 1 || qs[0].Post != "post_1" {
		t.Fatalf("stored questions = %+v", qs)
	}
}

// The owner can answer a question while it is being posted. If the post
// then fails, the answer stays and the ID is not given out again.
func TestAnswerDuringAFailedPostIsKept(t *testing.T) {
	s := &Store{Root: questionRoot(t)}
	posting, answered := make(chan Question), make(chan struct{})
	asked := make(chan error, 1)
	go func() {
		_, err := s.Ask(sample("a"), func(q Question) (string, error) {
			posting <- q
			<-answered
			return "", errors.New("post failed")
		})
		asked <- err
	}()
	q := <-posting
	if _, err := s.Answer(q.Room, q.ID, Answer{By: "owner", Choice: "yes"}); err != nil {
		t.Fatalf("answer the question being posted: %v", err)
	}
	close(answered)
	if err := <-asked; err == nil {
		t.Fatal("Ask passed a rejected post")
	}
	qs, err := s.List("room")
	if err != nil || len(qs) != 1 || qs[0].ID != q.ID || qs[0].Answer == nil {
		t.Fatalf("questions after the failed post = %+v, %v; want %s with its answer", qs, err, q.ID)
	}
	if next, err := s.Ask(sample("a"), nil); err != nil || next.ID == q.ID {
		t.Fatalf("next question = %+v, %v; must not reuse %s", next, err, q.ID)
	}
}
