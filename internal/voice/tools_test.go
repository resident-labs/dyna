package voice

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
)

var toolsNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// toolsFixture gives Voice's tools for room "work" in temp stores, with a
// second room "elsewhere" whose data must never reach them.
func toolsFixture(t *testing.T) (*Tools, room.Record, room.Record) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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
	tools.now = func() time.Time { return toolsNow }
	return tools, work, other
}

func writePRs(t *testing.T, records ...*state.Record) {
	t.Helper()
	st := state.State{Version: 1, Records: map[string]*state.Record{}}
	for _, r := range records {
		r.Key = r.Host + "/" + r.Repo + "#" + itoa(r.Number)
		r.URL = "https://" + r.Host + "/" + r.Repo + "/pull/" + itoa(r.Number)
		r.Watching, r.Created = true, toolsNow
		st.Records[r.Key] = r
	}
	if err := state.Write(watch.DefaultStore().Root, st); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// assertSpoken checks that an answer is short speech: one to three
// sentences, no paths, URLs or hashes, and nothing from the other room.
func assertSpoken(t *testing.T, got string) {
	t.Helper()
	if n := strings.Count(got, ". ") + 1; got == "" || n > 3 || len(got) > 400 {
		t.Errorf("not short speech (%d sentences): %q", n, got)
	}
	for _, bad := range []string{"/", "abc1234", "0ad3892e", "Secret", "stranger", "\n", "|"} {
		if strings.Contains(got, bad) {
			t.Errorf("answer contains %q: %q", bad, got)
		}
	}
}

func TestPRStatusOfTheRoomsPR(t *testing.T) {
	tools, work, other := toolsFixture(t)
	writePRs(t,
		&state.Record{Host: "github.com", Repo: "o/r", Number: 7, Title: "Give workers their port env", RoomID: work.ID, State: "open", Head: "abc1234def", RoundHead: "abc1234def", Verdict: "approve", CI: "passed"},
		&state.Record{Host: "github.com", Repo: "o/r", Number: 8, Title: "Old work", RoomID: work.ID, State: "merged"},
		&state.Record{Host: "github.com", Repo: "o/r", Number: 9, Title: "Secret other room work", RoomID: other.ID, State: "open", CI: "failed"},
	)
	got, err := tools.PRStatus(7)
	if err != nil {
		t.Fatal(err)
	}
	assertSpoken(t, got)
	for _, want := range []string{"PR 7, Give workers their port env, is open.", "CI passed", "the review approved it", "first in the merge queue"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %q", want, got)
		}
	}
	if got, _ := tools.PRStatus(8); got != "PR 8, Old work, has merged." {
		t.Errorf("merged: %q", got)
	}
	// Another room's PR is unknown here.
	if got, _ := tools.PRStatus(9); got != "I don't see PR 9 in this room." {
		t.Errorf("other room: %q", got)
	}
}

func TestMergeQueueCountsOnlyTheRoomsPRs(t *testing.T) {
	tools, work, other := toolsFixture(t)
	if got, _ := tools.MergeQueue(); got != "The merge queue is empty." {
		t.Errorf("empty: %q", got)
	}
	writePRs(t,
		&state.Record{Host: "github.com", Repo: "o/r", Number: 7, Title: "Give workers their port env", RoomID: work.ID, State: "open", Head: "abc1234def", CI: "pending"},
		&state.Record{Host: "github.com", Repo: "o/r", Number: 8, Title: "Fix the parser", RoomID: work.ID, State: "open", Head: "fed4321cba", CI: "pending"},
		&state.Record{Host: "github.com", Repo: "o/r", Number: 9, Title: "Secret other room work", RoomID: other.ID, State: "open", CI: "pending"},
	)
	got, err := tools.MergeQueue()
	if err != nil {
		t.Fatal(err)
	}
	assertSpoken(t, got)
	if !strings.HasPrefix(got, "2 PRs are in the merge queue: 0 landing and 2 waiting. Next is PR ") {
		t.Errorf("queue: %q", got)
	}
}

func TestMergeQueueText(t *testing.T) {
	got := mergeQueueText(watch.MergeControlStatus{Paused: true, Queue: []watch.MergeQueueItem{
		{ChangeID: "github.com/o/r#12", Title: "Land me", State: "trial-running"},
		{ChangeID: "local-3f9a2b7c", State: "review", Reason: "needs review on the current head"},
	}})
	want := "Merging is paused in this room. 2 PRs are in the merge queue: 1 landing and 1 waiting. Next is PR 12, Land me, landing now."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Only a trial that is still moving counts as landing. A rejected, failed or
// unavailable trial keeps its reason, in the queue summary and in PR status.
func TestTrialStatesInQueueAndPRStatus(t *testing.T) {
	for _, c := range []struct{ state, reason, phrase string }{
		{"trial-running", "", "landing now"},
		{"trial-publishing", "", "landing now"},
		{"trial-rejected", "merge trial failed on the parser test", "merge trial failed on the parser test"},
		{"trial-unavailable", "requires confirmed push CI for trial branches", "requires confirmed push CI for trial branches"},
		{"trial-failed", "batch trial failed; waiting for root", "batch trial failed; waiting for root"},
		{"trial-awaiting-root", "", "trial awaiting root"},
	} {
		item := watch.MergeQueueItem{ChangeID: "github.com/o/r#12", URL: "https://github.com/o/r/pull/12", Title: "Land me", State: c.state, Reason: c.reason}
		landing := 0
		if c.phrase == "landing now" {
			landing = 1
		}
		queue := mergeQueueText(watch.MergeControlStatus{Queue: []watch.MergeQueueItem{item}})
		want := fmt.Sprintf("One PR is in the merge queue: %d landing and %d waiting. Next is PR 12, Land me, %s.", landing, 1-landing, c.phrase)
		if queue != want {
			t.Errorf("%s queue:\n got  %q\n want %q", c.state, queue, want)
		}
		pr := prStatusText(watch.Record{Number: 12, Title: "Land me", URL: item.URL, State: "open", CI: "passed"}, []watch.MergeQueueItem{item})
		if !strings.HasSuffix(pr, "It is first in the merge queue, "+c.phrase+".") {
			t.Errorf("%s PR status: %q", c.state, pr)
		}
	}
}

func TestSpokenDropsPathsAndHashes(t *testing.T) {
	got := spoken("See /tmp/turn-3.txt, https://github.com/o/r/pull/7 and internal/voice/tools.go at 0ad3892e505c for @root. Then ses_20261019-195703-82eb65a8 is done.")
	if want := "See and at for root. Then is done."; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := ago(90 * time.Minute); got != "One hour ago" {
		t.Errorf("ago: %q", got)
	}
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

// A request said again while it is still under way is a duplicate: it is not
// posted again. Once the call has its answer, the same words start it anew.
func TestDispatchSendsARepeatOnlyAfterItsAnswer(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	rooms := room.DefaultStore()
	asks := func() int {
		msgs, err := rooms.Messages(work.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, msg := range msgs {
			if msg.From == "work:voice-call" && msg.Kind != "voice" {
				n++
			}
		}
		return n
	}
	if got, err := tools.Dispatch(t.Context(), "Check CI on PR 7."); err != nil || got != workStarted {
		t.Fatalf("first: %q, %v", got, err)
	}
	if got, err := tools.Dispatch(t.Context(), "check ci on  PR 7"); err != nil || got != workDuplicate {
		t.Fatalf("again: %q, %v; want duplicate", got, err)
	}
	if got, _ := tools.Dispatch(t.Context(), "Merge PR 8"); got != workStarted {
		t.Fatalf("another request: %q", got)
	}
	if n := asks(); n != 2 {
		t.Fatalf("%d requests posted, want 2", n)
	}
	if _, err := rooms.Post(work.ID, room.PostInput{From: "work", To: []string{"work:voice-call"}, Kind: "message", Text: "CI on PR 7 is green."}); err != nil {
		t.Fatal(err)
	}
	if got, _ := tools.Dispatch(t.Context(), "Check CI on PR 7"); got != workStarted || asks() != 3 {
		t.Fatalf("after the answer: %q, %d posted; want it sent again", got, asks())
	}
}

// The owner's spoken answer goes to the open question: words that name an
// option choose it, other words are the answer's text.
func TestAnswerQuestionAnswersTheOpenQuestion(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	svc := tools.link.svc
	root := notify.Sender{Kind: notify.SenderOwner, Room: work.Name}
	if _, err := tools.AnswerQuestion(t.Context(), "", "yes"); err == nil {
		t.Fatal("answered with no open question")
	}
	q, err := svc.Ask(t.Context(), root, work.ID, "owner", "Merge PR 7 now?", "Review other PRs.", []string{"yes", "no"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tools.AnswerQuestion(t.Context(), "", "Yes."); err != nil || got != questionAnswered {
		t.Fatalf("answer: %q, %v", got, err)
	}
	qs, err := svc.QuestionStore().List(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range qs {
		if got.ID == q.ID && (got.State != "answered" || got.Answer == nil || got.Answer.Choice != "yes" || got.Answer.By != "owner") {
			t.Fatalf("question = %+v, want answered yes by the owner", got)
		}
	}
	free, err := svc.Ask(t.Context(), root, work.ID, "owner", "Which day works?", "Review other PRs.", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.AnswerQuestion(t.Context(), free.ID, "Thursday after lunch"); err != nil {
		t.Fatal(err)
	}
	qs, _ = svc.QuestionStore().List(work.ID)
	for _, got := range qs {
		if got.ID == free.ID && (got.Answer == nil || got.Answer.Text != "Thursday after lunch" || got.Answer.Choice != "") {
			t.Fatalf("question = %+v, want the owner's words", got)
		}
	}
}

// With two open questions, the owner's answer goes to the one Voice read
// out, by its ID; without an ID it is refused rather than guessed.
func TestAnswerQuestionNeedsTheIDWhenSeveralAreOpen(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	svc := tools.link.svc
	root := notify.Sender{Kind: notify.SenderOwner, Room: work.Name}
	first, err := svc.Ask(t.Context(), root, work.ID, "owner", "Merge PR 7 now?", "Review other PRs.", []string{"yes", "no"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Ask(t.Context(), root, work.ID, "owner", "Which day works?", "Review other PRs.", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools.AnswerQuestion(t.Context(), "", "Thursday"); err == nil || !strings.Contains(err.Error(), "question_id") {
		t.Fatalf("an answer without an ID with two open: %v, want refused", err)
	}
	if !strings.Contains(questionPrompt(second), "question_id "+second.ID) {
		t.Fatalf("the question prompt does not give Voice its ID: %q", questionPrompt(second))
	}
	if _, err := tools.AnswerQuestion(t.Context(), second.ID, "Thursday"); err != nil {
		t.Fatal(err)
	}
	qs, _ := svc.QuestionStore().List(work.ID)
	for _, q := range qs {
		switch q.ID {
		case first.ID:
			if q.State != "open" {
				t.Fatalf("the first question was answered: %+v", q)
			}
		case second.ID:
			if q.Answer == nil || q.Answer.Text != "Thursday" {
				t.Fatalf("the second question = %+v, want answered Thursday", q)
			}
		}
	}
}
