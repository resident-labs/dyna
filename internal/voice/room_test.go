package voice

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/notify"
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
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	go link.Watch(ctx, since, call)
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
	go link.Watch(t.Context(), time.UnixMilli(time.Now().UnixMilli()), call)
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
	said  chan string // the text of each Speak
	other chan string // "progress <task> <text>" and "end <task>"
}

func newFakeSpeaker() *fakeSpeaker {
	return &fakeSpeaker{said: make(chan string, 8), other: make(chan string, 8)}
}

func (f *fakeSpeaker) Speak(_, _, text string) { f.said <- text }
func (f *fakeSpeaker) Progress(task string, _ time.Time, text string) {
	f.other <- "progress " + task + " " + text
}
func (f *fakeSpeaker) EndProgress(task string) { f.other <- "end " + task }

// Incidental room traffic never becomes spoken progress. Multiple explicit
// call posts still reach Voice, including corrections and distinct results.
func TestWatchIgnoresIncidentalProgress(t *testing.T) {
	tools, work, _ := toolsFixture(t)
	externalRoot(t, work)
	link, rooms := tools.link, room.DefaultStore()
	call := newFakeSpeaker()
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	ended := make(chan error, 1)
	go func() { ended <- link.Watch(ctx, time.Time{}, call) }()
	if _, err := link.Ask(t.Context(), "Fix CI on PR 7."); err != nil {
		t.Fatal(err)
	}
	post := func(from, text string, to ...string) {
		t.Helper()
		if _, err := rooms.Post(work.ID, room.PostInput{From: from, To: to, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	post("work:builder", "Rerunning a different job.", "work")
	post("work:root", "Check another PR.", "work:builder")
	post("work", "Check the release.", "work:helper")
	post("work:builder", "Review the logs.", "work:helper")
	for _, text := range []string{"CI is green.", "Correction: one job is still running.", "The review passed."} {
		post("work", text, "work:voice-call")
		if got := <-call.said; !strings.HasSuffix(got, text) {
			t.Fatalf("said %q, want explicit call post %q", got, text)
		}
	}
	stop()
	<-ended // every earlier room post has been handled
	select {
	case got := <-call.other:
		t.Fatalf("incidental progress reached the call: %q", got)
	default:
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
