package roomsvc

import (
	"encoding/json"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/state"
	"path/filepath"
)

func TestQuestionViewsAndEvents(t *testing.T) {
	e := newEnv(t)
	rec := e.room("questions")
	snd := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	sig := map[string]string{}
	var version string
	e.svc.scanQuestions(sig, &version, true, func(Event) { t.Fatal("initial event") })
	q, err := e.svc.Notify.Ask(e.ctx, snd, "", "", "Which region?", "check data", []string{"east", "west"}, []notify.File{{Name: "region.html", Render: room.RenderHTML, Data: []byte("<html><title>Regions</title><body>East and West</body></html>")}})
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	e.svc.scanQuestions(sig, &version, false, func(ev Event) { events = append(events, ev) })
	if len(events) != 1 || events[0].Kind != "questions" || events[0].RoomID != rec.ID {
		t.Fatal(events)
	}
	e.svc.scanQuestions(sig, &version, false, func(Event) { t.Fatal("unchanged state emitted") })
	qs, err := e.svc.Questions(rec.ID)
	if err != nil || len(qs[rec.ID]) != 1 || qs[rec.ID][0].AskerName != "root" || len(qs[rec.ID][0].Attachments) != 1 {
		t.Fatal(qs, err)
	}
	data, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Change *struct {
			Op   string
			ID   string
			Item Question
		}
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Change == nil || wire.Change.Op != "upsert" || wire.Change.ID != q.ID || wire.Change.Item.ID != q.ID || len(wire.Change.Item.Attachments) != 1 || wire.Change.Item.AskerName != qs[rec.ID][0].AskerName {
		t.Fatalf("question event did not carry its canonical row: %s", data)
	}
	visuals, err := e.svc.Visuals(rec.ID)
	if err != nil || len(visuals) != 1 || visuals[0].Post.ID != q.Post || visuals[0].Attachment.Title != "Regions" {
		t.Fatal(visuals, err)
	}
	if err = e.svc.Notify.AnswerQuestion(e.ctx, notify.Sender{Kind: notify.SenderOwner, Human: true}, rec.ID, q.ID, "east", ""); err != nil {
		t.Fatal(err)
	}
	e.svc.scanQuestions(sig, &version, false, func(ev Event) { events = append(events, ev) })
	if len(events) != 2 {
		t.Fatal(events)
	}
	data, err = json.Marshal(events[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Change == nil || wire.Change.Op != "upsert" || wire.Change.ID != q.ID || wire.Change.Item.Answer == nil {
		t.Fatalf("answered question event lost its canonical row: %s", data)
	}
	open, err := e.svc.Notify.QuestionStore().OpenAskedBy(rec.ID, "owner")
	if err != nil || len(open) != 0 {
		t.Fatal(open, err)
	}
	// A different room must never see this question or card.
	other := e.room("other")
	qs, err = e.svc.Questions(other.ID)
	if err != nil || len(qs[other.ID]) != 0 {
		t.Fatal(qs, err)
	}
	if err = e.svc.Notify.AnswerQuestion(e.ctx, notify.Sender{Kind: notify.SenderOwner, Human: true}, other.ID, q.ID, "east", ""); err == nil {
		t.Fatal("cross-room answer accepted")
	}
	if _, err = e.svc.Notify.QuestionStore().Ask(questions.Question{Room: rec.ID, Asker: "session", Text: "Why?", Meanwhile: "test"}, nil); err != nil {
		t.Fatal(err)
	}
	open, err = e.svc.Notify.QuestionStore().OpenAskedBy(rec.ID, "session")
	if err != nil || len(open) != 1 {
		t.Fatal(open, err)
	}
}

// Root and the human share the owner identity. The human flag, as on
// posts, names each "root" or "you" in the question view and answer posts.
func TestQuestionsTellRootFromHuman(t *testing.T) {
	e := newEnv(t)
	rec := e.room("questions")
	root := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	ask := func(text string) questions.Question {
		q, err := e.svc.Notify.Ask(e.ctx, root, "", "", text, "wait", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	byHuman, byRoot := ask("Ship it?"), ask("Rebase first?")
	if err := e.svc.Notify.AnswerQuestion(e.ctx, notify.Sender{Kind: notify.SenderOwner, Human: true}, rec.ID, byHuman.ID, "", "yes"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Notify.AnswerQuestion(e.ctx, root, rec.ID, byRoot.ID, "", "no"); err != nil {
		t.Fatal(err)
	}
	qs, err := e.svc.Questions(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string][2]string{}
	for _, q := range qs[rec.ID] {
		names[q.ID] = [2]string{q.AskerName, q.AnswererName}
	}
	if names[byHuman.ID] != [2]string{"root", "you"} || names[byRoot.ID] != [2]string{"root", "root"} {
		t.Fatalf("asker and answerer names = %v", names)
	}
	msgs, err := e.svc.Notify.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]bool{}
	for _, m := range msgs {
		if m.ID == questions.PostID(rec.ID, byHuman.ID, "answer") || m.ID == questions.PostID(rec.ID, byRoot.ID, "answer") {
			answers[m.ID] = m.Human
		}
	}
	if !answers[questions.PostID(rec.ID, byHuman.ID, "answer")] || answers[questions.PostID(rec.ID, byRoot.ID, "answer")] || len(answers) != 2 {
		t.Fatalf("answer posts human flags = %v", answers)
	}
	// The withdrawal names whoever withdrew, not the asker.
	store := e.svc.Notify.QuestionStore()
	asked, err := store.Ask(questions.Question{Room: rec.ID, Asker: "owner", Human: true, Text: "Pause the deploy?", Meanwhile: "wait"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Withdraw(rec.ID, asked.ID, "owner", false, "not needed"); err != nil {
		t.Fatal(err)
	}
	qs, err = e.svc.Questions(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs[rec.ID] {
		if q.ID == asked.ID && (q.AskerName != "you" || q.WithdrawnBy != "root") {
			t.Fatalf("asker %q, withdrawn by %q", q.AskerName, q.WithdrawnBy)
		}
	}
}

// The owner may withdraw a question the owner asked. The withdrawal post
// then goes to the owner, like any post to @root alone.
func TestOwnerWithdrawsOwnQuestion(t *testing.T) {
	e := newEnv(t)
	rec := e.room("questions")
	root := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	q, err := e.svc.Notify.Ask(e.ctx, root, "", "", "Pause the deploy?", "wait", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.svc.Notify.WithdrawQuestion(root, rec.ID, q.ID, "not needed"); err != nil {
		t.Fatal(err)
	}
	post, err := e.svc.Notify.Rooms.Message(rec.ID, questions.PostID(rec.ID, q.ID, "withdrawn"))
	if err != nil || post.From != rec.Name || len(post.To) != 1 || post.To[0] != rec.Name || post.ReplyTo != q.Post {
		t.Fatalf("withdrawal post = %+v, %v", post, err)
	}
}

func TestLegacyQuestionAnswersDoNotDuplicateFeed(t *testing.T) {
	e := newEnv(t)
	rec := e.room("legacy")
	for _, p := range []room.PostInput{
		{From: "legacy:child", Kind: "question", To: []string{"legacy:lead"}, Text: "[QUESTION q1] Which fix?"},
		{From: "legacy:lead", Kind: "message", To: []string{"legacy:child"}, Text: "[ANSWER q1] Small fix."},
		{From: "legacy:lead", Kind: "question-answered", To: []string{rec.Name}, Text: "q1 answered: Small fix."},
	} {
		if _, err := e.svc.Notify.Rooms.Post(rec.ID, p); err != nil {
			t.Fatal(err)
		}
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil || len(feed.Posts) != 2 {
		t.Fatalf("feed: %+v, %v", feed, err)
	}
	incremental, _, err := e.svc.FeedSince(rec.ID, 0)
	if err != nil || len(incremental) != 2 {
		t.Fatalf("incremental: %+v, %v", incremental, err)
	}
	msgs, _ := e.svc.Notify.Rooms.Messages(rec.ID)
	for _, addr := range []string{"legacy:child", "legacy:lead"} {
		posts, err := e.svc.sessionPosts(rec, msgs, "", addr, time.Time{}, time.Time{}, e.svc.avatars(rec.ID))
		if err != nil || len(posts) != 2 {
			t.Fatalf("%s posts: %+v, %v", addr, posts, err)
		}
	}
}

func TestLegacyWithdrawalKeepsOriginalParticipants(t *testing.T) {
	e := newEnv(t)
	rec := e.room("legacy-withdrawal")
	q, err := e.svc.Notify.QuestionStore().Ask(questions.Question{Room: rec.ID, Asker: "child", To: "lead", Text: "Which fix?", Meanwhile: "test"}, func(q questions.Question) (string, error) {
		p, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: "legacy-withdrawal:child", Kind: "question", To: []string{"legacy-withdrawal:lead"}, Text: "[QUESTION " + q.ID + "] Which fix?"})
		return p.ID, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Notify.QuestionStore().Withdraw(rec.ID, q.ID, "child", false, "done"); err != nil {
		t.Fatal(err)
	}
	event, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: rec.Name, Kind: "question-withdrawn", To: []string{rec.Name}, Text: q.ID + " withdrawn: done"})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	p := feed.Posts[len(feed.Posts)-1]
	if p.ID != event.ID || p.From != "legacy-withdrawal:child" || p.Addressed(rec.Name) || !p.Addressed("legacy-withdrawal:lead") || p.Kind != "message" {
		t.Fatal(p)
	}
	msgs, _ := e.svc.Notify.Rooms.Messages(rec.ID)
	posts, err := e.svc.sessionPosts(rec, msgs, "", "legacy-withdrawal:lead", time.Time{}, time.Time{}, e.svc.avatars(rec.ID))
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts: %+v, %v", posts, err)
	}
	ids, err := e.svc.ownerPosts(rec)
	if err != nil || len(ids) != 0 {
		t.Fatalf("owner unread IDs: %v, %v", ids, err)
	}
}

func TestQuestionWriteEmitsOneCanonicalEvent(t *testing.T) {
	e := newEnv(t)
	rec := e.room("question-events")
	sig := map[string]string{}
	var version string
	e.svc.scanQuestions(sig, &version, true, func(Event) { t.Fatal("initial event") })
	db, err := database.Open(filepath.Dir(e.svc.Notify.QuestionStore().Root))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	state.For(db).SetPublish(func(event hub.Event) {
		if event.Topic == "questions" {
			e.svc.StoreChanged(event.Topic)
		}
	})
	if _, err := e.svc.Notify.QuestionStore().Ask(questions.Question{Room: rec.ID, Asker: "owner", Text: "Which region?", Meanwhile: "check data"}, nil); err != nil {
		t.Fatal(err)
	}
	e.svc.scanQuestions(sig, &version, false, func(event Event) {
		if event.Kind == "questions" {
			count++
			if event.RoomID != rec.ID || event.Change == nil {
				t.Errorf("incomplete question event: %+v", event)
			}
		}
	})
	if count != 1 {
		t.Fatalf("one question write emitted %d events; want 1", count)
	}
}
