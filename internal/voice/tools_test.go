package voice

import (
	"context"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

// toolsFixture gives Voice's tools for room "work" in temp stores, with a
// second room "elsewhere" whose data must never reach them.
func toolsFixture(t *testing.T) (*Tools, room.Record, room.Record) {
	t.Helper()
	root := storetest.DataDir(t)
	t.Cleanup(func() {
		if err := database.Close(root); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rooms := room.DefaultStore()
	work, err := rooms.Create(room.CreateInput{Name: "work", CreatedBy: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "elsewhere", CreatedBy: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := OpenRoom(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	tools := NewTools(link)
	return tools, work, other
}

// externalRoot gives the room's root seat to an external orchestrator, so
// requests to root are stored without starting an internal root.
func externalRoot(t *testing.T, rec room.Record) {
	t.Helper()
	if _, _, err := room.DefaultStore().SetSeat(t.Context(), rec.ID, room.Seat{External: true, Harness: "test"}, nil); err != nil {
		t.Fatal(err)
	}
}

// Voice speaks only what root posts to @voice-call: dispatch posts the
// request once and returns, and root's turn report stays in the room.
func TestDelegatePostsOnceAndNeverRelaysRootsReport(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	rooms := room.DefaultStore()
	got, err := tools.Dispatch(t.Context(), "check CI on PR 7")
	if err != nil || got != workStarted {
		t.Fatalf("dispatch: %q, %v", got, err)
	}
	if _, err := rooms.Post(work.ID, room.PostInput{From: "work:root", To: []string{"work"}, Kind: "completed", Text: "work:root turn 1 completed. Result: CI is green."}); err != nil {
		t.Fatal(err)
	}
	msgs, err := rooms.Messages(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fromVoice []room.Message
	for _, msg := range msgs {
		if msg.From == "work:voice-call" {
			fromVoice = append(fromVoice, msg)
		}
	}
	if len(fromVoice) != 1 || fromVoice[0].Text != "check CI on PR 7" || !fromVoice[0].Wake || fromVoice[0].Source != room.SourceVoice {
		t.Fatalf("Voice's posts = %+v, want only the request", fromVoice)
	}
	if _, err := tools.Dispatch(t.Context(), "  "); err == nil {
		t.Fatal("an empty request was sent")
	}
}

// Repeated words are valid steering. Every call forwards exactly what the
// owner said rather than suppressing a request that is already running.
func TestDispatchPreservesWordsAndSteering(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	text := "Check CI on PR 7.\nKeep the review open."
	for range 2 {
		if _, err := run(t, tools.Realtime(t.Context()), t.Context(), "dispatch", `{"prompt":"Check CI on PR 7.\nKeep the review open."}`); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := room.DefaultStore().Messages(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != text || msgs[1].Text != text {
		t.Fatalf("forwarded messages: %+v", msgs)
	}
}

// Voice dispatches a question answer; root records the selected question
// with the ordinary question API and preserves the owner's words.
func TestDispatchedQuestionAnswerIsRecordedByRoot(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	svc := tools.link.svc
	root := notify.Sender{Kind: notify.SenderOwner, Room: work.Name}
	q, err := svc.Ask(t.Context(), root, work.ID, "owner", "Which day?", "Review the code.", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const answer = "Thursday after lunch"
	if _, err := tools.Dispatch(t.Context(), answer); err != nil {
		t.Fatal(err)
	}
	qs, err := svc.QuestionStore().List(work.ID)
	if err != nil || len(qs) != 1 || qs[0].State != "open" {
		t.Fatalf("dispatch changed the question: %+v %v", qs, err)
	}
	if err := svc.AnswerQuestion(context.Background(), root, work.ID, q.ID, "", answer); err != nil {
		t.Fatal(err)
	}
	qs, err = svc.QuestionStore().List(work.ID)
	if err != nil || len(qs) != 1 || qs[0].Answer == nil || qs[0].Answer.Text != answer || qs[0].State != "answered" {
		t.Fatalf("root did not record the answer: %+v %v", qs, err)
	}
}
