package voice

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/parentoutput"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

// The call's posts go between the owner and Voice and never wake anyone. A
// request goes from Voice to @root and wakes root. All carry the voice source.
func TestCallPostsAreOwnerVoiceDMs(t *testing.T) {
	link := &RoomLink{rec: room.Record{Name: "r"}}
	owner := link.callPost(true, room.PostInput{Spoken: true, Text: "check ci"})
	voice := link.callPost(false, room.PostInput{RelayOf: "m9", Text: "CI is green."})
	if owner.From != "r" || owner.To[0] != "r:voice-call" || voice.From != "r:voice-call" || voice.To[0] != "r" {
		t.Fatalf("direction: %+v %+v", owner, voice)
	}
	for _, in := range []room.PostInput{owner, voice} {
		if in.Wake || in.Kind != "voice" || in.Source != room.SourceVoice {
			t.Fatalf("call post: %+v", in)
		}
	}
	if !owner.Human || voice.Human {
		t.Fatalf("only the owner's own speech is Human: %+v %+v", owner, voice)
	}
	if voice.RelayOf != "m9" || !owner.Spoken {
		t.Fatalf("fields lost: %+v %+v", owner, voice)
	}
}

// Watch hands the call root's open questions, then root's @voice-call posts
// from the call's start, and nothing else.
func TestWatchSaysRootsQuestionsAndCallPosts(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	link, svc := tools.link, tools.link.svc
	root := notify.Sender{Kind: notify.SenderOwner, Room: work.Name} // an external root
	if _, err := svc.Ask(t.Context(), root, work.ID, "owner", "Merge PR 7 now?", "Review other PRs.", []string{"yes", "no"}, nil); err != nil {
		t.Fatal(err)
	}
	old, err := svc.Post(t.Context(), notify.PostInput{Sender: root, Mentions: []string{"@voice-call"}, Text: "An old ring."})
	if err != nil {
		t.Fatal(err)
	}
	since := time.UnixMilli(old.Message.TS + 1)
	for time.Now().Before(since) { // the next post must come after the call's start
	}
	call := newFakeSpeaker()
	said := call.said
	watchRoom(t, link, since, call)
	if got := <-said; !strings.Contains(got, "Merge PR 7 now?") || !strings.Contains(got, "yes, no") {
		t.Fatalf("first said %q, want root's open question with its options", got)
	}
	if err := link.Spoken(true, "Hello Voice."); err != nil { // the call's own transcript
		t.Fatal(err)
	}
	if _, err := svc.Post(t.Context(), notify.PostInput{Sender: root, Mentions: []string{"@voice-call"}, Text: "CI is red. Can you talk?"}); err != nil {
		t.Fatal(err)
	}
	if got := <-said; !strings.HasSuffix(got, "CI is red. Can you talk?") {
		t.Fatalf("then said %q, want only root's new call post", got)
	}
}

// A file root shares in the call reaches Voice by what it is and its title,
// so Voice says one line about it. A post with only a file has no result.
func TestWatchNamesWhatRootShares(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	link, svc := tools.link, tools.link.svc
	root := notify.Sender{Kind: notify.SenderOwner, Room: work.Name}
	call := newFakeSpeaker()
	// Posts carry millisecond timestamps: a start with finer precision would
	// make a post in the same millisecond look older and be skipped.
	watchRoom(t, link, time.UnixMilli(time.Now().UnixMilli()), call)
	card := notify.File{Name: "compare.html", Render: room.RenderHTML, Data: []byte("<!doctype html><title>Provider comparison</title><p>five providers</p>")}
	if _, err := svc.Post(t.Context(), notify.PostInput{Sender: root, Mentions: []string{"@voice-call"}, Text: "I compared the providers.", Files: []notify.File{card, {Name: "ci.png", Data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R'}}}}); err != nil {
		t.Fatal(err)
	}
	got := <-call.said
	if !strings.Contains(got, "I compared the providers.") || !strings.Contains(got, `a card titled "Provider comparison"; an image named "ci.png"`) {
		t.Fatalf("said %q, want root's text, then the shared card and image by name", got)
	}
	if only := tellPrompt("", []room.Attachment{{Name: "notes.txt"}}); strings.Contains(only, "work result") || !strings.Contains(only, `a file named "notes.txt"`) {
		t.Fatalf("a post with only a file asks %q", only)
	}
}

// fakeSpeaker records what Watch hands the call.
type fakeSpeaker struct {
	said    chan string // the text of each Speak
	context chan string // execution context that requests no speech
}

func newFakeSpeaker() *fakeSpeaker {
	return &fakeSpeaker{said: make(chan string, 8), context: make(chan string, 8)}
}

func (f *fakeSpeaker) Speak(_, _, text string) { f.said <- text }
func (f *fakeSpeaker) Context(text string)     { f.context <- text }

// watchRoom joins the watch before the fixture's data directory is removed.
func watchRoom(t *testing.T, link *RoomLink, since time.Time, call Speaker) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = link.Watch(ctx, since, call)
	}()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

// Root traffic for running work is context and never requests speech. Multiple explicit
// call posts still reach Voice, including corrections and distinct results.
func TestWatchKeepsExecutionTrafficAsSilentContext(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	link, rooms := tools.link, room.DefaultStore()
	call := newFakeSpeaker()
	if _, err := link.Ask(t.Context(), "Fix CI on PR 7."); err != nil {
		t.Fatal(err)
	}
	postKind := func(from, kind, text string, to ...string) {
		t.Helper()
		if _, err := rooms.Post(work.ID, room.PostInput{From: from, Kind: kind, To: to, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	post := func(from, text string, to ...string) { postKind(from, "", text, to...) }
	post("work:builder", "A root-directed result.", "work:root")
	post("work:root", "Root without an agent recipient.", "work")
	post("work", "External root status.", "work")
	post("work:builder", "DROP agent traffic.", "work:helper")
	post("work:builder", "DROP mixed call traffic.", "work:root", "work:voice-call")
	postKind("work:root", "voice", "DROP voice transcript.", "work:builder")
	post("work:root", "Mixed call update.", "work:builder", "work:voice-call")
	post("work:root", "FIRST HEAD "+strings.Repeat("α", 700), "work")
	post("work:root", "MIDDLE "+strings.Repeat("漢", 650), "work")
	if _, err := link.Ask(t.Context(), "Also check the tests."); err != nil {
		t.Fatal(err)
	}
	post("work:root", "MORE "+strings.Repeat("x", 1400), "work")
	post("work:root", "TAIL "+strings.Repeat("🙂", 600)+" LAST ROOT POINT", "work")
	postKind("work:root", "completed", "Finished first turn.", "work")
	post("work:builder", "DROP traffic after completion.", "work")
	texts := []string{"CI is green.", "Correction: one job is still running.", "The review passed."}
	for _, text := range texts {
		post("work", text, "work:voice-call")
	}
	if _, err := link.Ask(t.Context(), "Check another request."); err != nil {
		t.Fatal(err)
	}
	post("work:root", "SECOND HEAD "+strings.Repeat("é", 2600)+" SECOND TAIL", "work")
	postKind("work:root", "stopped", "Finished second turn.", "work")
	post("work", "Second request ended.", "work:voice-call")

	// Follow reads the complete existing log. Spoken posts are ordering
	// barriers; all earlier context is recorded before Watch stops.
	stop := watchRoom(t, link, time.Time{}, call)
	for _, text := range append([]string{"Mixed call update."}, append(texts, "Second request ended.")...) {
		if got := <-call.said; !strings.HasSuffix(got, text) {
			t.Fatalf("said %q, want call post %q", got, text)
		}
	}
	stop()
	var chunks []string
	for {
		select {
		case text := <-call.context:
			chunks = append(chunks, text)
		default:
			goto collected
		}
	}
collected:
	var requests [2]string
	request := 0
	for _, chunk := range chunks {
		if !strings.HasPrefix(chunk, "[BACKEND] ") || !utf8.ValidString(chunk) {
			t.Fatalf("invalid context chunk %q", chunk)
		}
		if strings.Contains(chunk, "DROP") || strings.Contains(chunk, "Mixed call update.") {
			t.Fatalf("unrelated or call post became silent context: %q", chunk)
		}
		if request >= len(requests) {
			t.Fatal("context continued after both requests ended")
		}
		requests[request] += chunk
		if strings.Contains(chunk, "Finished first turn.") || strings.Contains(chunk, "Finished second turn.") {
			request++
		}
	}
	if request != 2 {
		t.Fatalf("got %d completed context streams, want 2", request)
	}
	for i, text := range requests {
		if len(text) > 4000 {
			t.Fatalf("request %d sent %d bytes, exceeding its shared budget", i, len(text))
		}
		if strings.Count(text, "\n…output truncated…\n") != 1 {
			t.Fatalf("request %d lacks one truncation marker", i)
		}
	}
	for _, want := range []string{"A root-directed result.", "Root without an agent recipient.", "External root status.", "FIRST HEAD", "LAST ROOT POINT"} {
		if !strings.Contains(requests[0], want) {
			t.Fatalf("first request lost %q", want)
		}
	}
	if !strings.Contains(requests[1], "SECOND HEAD") || !strings.Contains(requests[1], "SECOND TAIL") || strings.Contains(requests[1], "FIRST HEAD") {
		t.Fatal("second request did not get a fresh head/tail budget")
	}
}

// A live call hears the final result of a root turn that reports to the
// owner. A call that has ended hears nothing, so the feed never speaks work
// the owner did not ask Voice about.
func TestWatchSaysRootTurnResultsOnlyWhileLive(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	link, rooms := tools.link, room.DefaultStore()
	result := func(turn int, text string) {
		t.Helper()
		if _, err := rooms.Post(work.ID, room.PostInput{From: work.Name + ":root", Kind: "completed",
			OwnerReport: true, Turn: turn, To: []string{work.Name}, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	call := newFakeSpeaker()
	stop := watchRoom(t, link, time.Time{}, call)
	result(1, "work:root turn 1 completed. Result: CI is green.")
	if got := <-call.said; !strings.HasPrefix(got, "[BACKEND] ") || !strings.Contains(got, "Result: CI is green.") {
		t.Fatalf("said %q, want root's turn result", got)
	}
	stop()
	result(2, "work:root turn 2 completed. Result: The review passed.")
	select {
	case got := <-call.said:
		t.Fatalf("a call that ended said %q", got)
	default:
	}
}

// A turn whose result root already relayed to @voice-call is not said twice.
func TestWatchSkipsTurnResultRootAlreadyRelayed(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	link, rooms := tools.link, room.DefaultStore()
	call := newFakeSpeaker()
	watchRoom(t, link, time.Time{}, call)
	post := func(in room.PostInput) {
		t.Helper()
		in.From = work.Name + ":root"
		if _, err := rooms.Post(work.ID, in); err != nil {
			t.Fatal(err)
		}
	}
	post(room.PostInput{Turn: 3, To: []string{work.Name + ":voice-call"}, Text: "The merge is done."})
	post(room.PostInput{Kind: "completed", OwnerReport: true, Turn: 3, To: []string{work.Name},
		Text: "work:root turn 3 completed. Result: The merge is done."})
	// A later call post is the barrier: Watch says it only after it has read
	// the completion, so a broken skip would speak between the two.
	post(room.PostInput{Turn: 4, To: []string{work.Name + ":voice-call"}, Text: "The review passed."})
	if got := <-call.said; !strings.HasSuffix(got, "The merge is done.") {
		t.Fatalf("first said %q, want root's own relay", got)
	}
	if got := <-call.said; !strings.HasSuffix(got, "The review passed.") {
		t.Fatalf("second said %q, want the later call post, so the completion came first", got)
	}
	select {
	case got := <-call.said:
		t.Fatalf("the turn result was said as well: %q", got)
	default:
	}
}

// The engine posts only the start of a long result and saves the rest, so the
// call hears the whole result, including its last point.
func TestWatchSaysTheWholeSavedResult(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	link, rooms := tools.link, room.DefaultStore()
	call := newFakeSpeaker()
	watchRoom(t, link, time.Time{}, call)
	path := filepath.Join(t.TempDir(), "parent-output", "turn-5.txt")
	post, err := parentoutput.Text(path, "work:root turn 5 completed. Result: FIRST POINT "+strings.Repeat("detail ", 400)+"LAST POINT")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(post, "LAST POINT") {
		t.Fatal("the test post was not bounded, so it proves nothing about reading the report")
	}
	if _, err := rooms.Post(work.ID, room.PostInput{From: work.Name + ":root", Kind: "completed", OwnerReport: true,
		Turn: 5, To: []string{work.Name}, Text: post}); err != nil {
		t.Fatal(err)
	}
	if got := <-call.said; !strings.Contains(got, "FIRST POINT") || !strings.Contains(got, "LAST POINT") {
		t.Fatalf("said %q, want the whole saved result", got)
	}
}

// A long result reaches the call clipped like Codex output: its start and end
// survive, with one truncation marker, and it stays inside the budget.
func TestWatchClipsLongTurnResults(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	link, rooms := tools.link, room.DefaultStore()
	call := newFakeSpeaker()
	watchRoom(t, link, time.Time{}, call)
	path := filepath.Join(t.TempDir(), "parent-output", "turn-4.txt")
	head, tail := "FIRST POINT "+strings.Repeat("α", 3000), strings.Repeat("漢", 3000)+" LAST POINT"
	post, err := parentoutput.Text(path, "work:root turn 4 completed. Result: "+head+" "+tail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Post(work.ID, room.PostInput{From: work.Name + ":root", Kind: "completed", OwnerReport: true,
		Turn: 4, To: []string{work.Name}, Text: post}); err != nil {
		t.Fatal(err)
	}
	got := <-call.said
	if len(got) > resultBudget+64 || !utf8.ValidString(got) {
		t.Fatalf("clipped result is %d bytes and valid UTF-8 is %v", len(got), utf8.ValidString(got))
	}
	if !strings.Contains(got, "FIRST POINT") || !strings.Contains(got, "LAST POINT") || strings.Count(got, "…output truncated…") != 1 {
		t.Fatalf("clipped result %q lost its start, its end or its one marker", got)
	}
}

// An answer that picks none of the options goes to root in the owner's own
// words: the prompt offers the options and never forces one.
func TestQuestionPromptKeepsTheOwnersOwnWords(t *testing.T) {
	got := questionPrompt(questions.Question{Text: "Merge PR 7 now?", Options: []string{"yes", "no"}})
	for _, want := range []string{"The options are: yes, no.", "may also answer in their own words", "in the owner's own words", "Never choose an option for the owner"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt %q lacks %q", got, want)
		}
	}
	if got := questionPrompt(questions.Question{Text: "Which branch?"}); strings.Contains(got, "options") {
		t.Errorf("a question with no options mentions options: %q", got)
	}
}

func TestConnectedCallJoinsRequestAndSaysReasonOnce(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	st := tools.link.svc.CallRequestStore()
	row, _, err := st.Ask(work.ID, "CI needs your sign-in", "root")
	if err != nil {
		t.Fatal(err)
	}
	call := newFakeSpeaker()
	tools.link.joinRequestedCall(call)
	saved, err := st.Get(work.ID, row.ID)
	if err != nil || saved.State != "joined" || saved.ClosedBy != "owner" {
		t.Fatalf("joined: %+v %v", saved, err)
	}
	select {
	case text := <-call.said:
		if !strings.HasPrefix(text, "Open the call in one line.") || !strings.Contains(text, "Root wants to talk about CI needs your sign-in") {
			t.Fatal(text)
		}
	default:
		t.Fatal("reason was not queued")
	}
	tools.link.joinRequestedCall(call)
	select {
	case text := <-call.said:
		t.Fatalf("reason repeated: %s", text)
	default:
	}
}

// Resume restores the same room's recent transcript without replaying old
// spoken updates or dispatching the saved owner request again.
func TestResumeRestoresBoundedContextWithoutReplay(t *testing.T) {
	tools, work, other := toolsFixture(t)
	link := tools.link
	externalRoot(t, work)
	started := time.Now().Add(-time.Minute).UnixMilli()
	if err := link.Spoken(true, "Please fix CI."); err != nil {
		t.Fatal(err)
	}
	if err := link.Spoken(false, strings.Repeat("α", 7000)+"saved answer"); err != nil {
		t.Fatal(err)
	}
	rooms := link.svc.Rooms
	if _, err := rooms.Post(other.ID, room.PostInput{From: other.Name, To: []string{other.Name + ":voice-call"}, Kind: "voice", Text: "OTHER ROOM"}); err != nil {
		t.Fatal(err)
	}
	if _, err := link.Ask(t.Context(), "Please fix CI."); err != nil {
		t.Fatal(err)
	}
	if err := link.Spoken(true, "Please check the next failure too."); err != nil {
		t.Fatal(err)
	}
	old, err := rooms.Post(work.ID, room.PostInput{From: work.Name + ":root", To: []string{work.Name + ":voice-call"}, Text: "Already spoken."})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := rooms.Post(work.ID, room.PostInput{From: work.Name + ":root", To: []string{work.Name + ":voice-call"}, Text: "Not yet heard."})
	if err != nil {
		t.Fatal(err)
	}
	before, err := rooms.Messages(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newCalls(t, false)
	c.Open = func(string) (*RoomLink, error) { return link, nil }
	st, err := c.Resume(work.ID, started, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.StartedAt != started || link.replayCursor != old.ID {
		t.Fatalf("resume changed call timestamps: %+v", st)
	}
	saved, err := link.resumeContext()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) > 12500 || !utf8.ValidString(saved) || !strings.Contains(saved, "saved answer") || strings.Contains(saved, "OTHER ROOM") {
		t.Fatalf("invalid saved context: bytes=%d", len(saved))
	}
	call := newFakeSpeaker()
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Follow visits saved posts before observing cancellation.
	if err := link.Watch(ctx, time.UnixMilli(started), call); err != context.Canceled {
		t.Fatalf("Watch: %v", err)
	}
	if len(call.said) != 1 || !strings.Contains(<-call.said, "Not yet heard.") {
		t.Fatal("resume skipped an unheard update or replayed a spoken update")
	}
	if got := link.cursorFor(old.ID, newer.ID); got != newer.ID {
		t.Fatal("an older update moved the cursor backwards")
	}
	if got := link.cursorFor(newer.ID, old.ID); got != newer.ID {
		t.Fatal("the next update did not advance the cursor")
	}
	if !strings.Contains(saved, "Submitted to root: Please fix CI.") || !strings.Contains(saved, "Owner: Please check the next failure too.") {
		t.Fatal("resume lost submitted requests or interrupted owner intent")
	}
	after, err := rooms.Messages(work.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("resume dispatched saved text: posts before=%d after=%d err=%v", len(before), len(after), err)
	}
}
