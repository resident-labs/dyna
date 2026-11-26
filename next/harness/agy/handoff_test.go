package agy

import (
	"bytes"
	"context"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy/acp"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// lastPrompt is the whole of the last prompt the agent received.
func lastPrompt(t *testing.T, r *rig) string {
	t.Helper()
	prompts := r.prompts(t)
	return strings.Join(prompts[len(prompts)-1], "\n")
}

func mirrorText(t *testing.T, h *Harness) string {
	t.Helper()
	data, err := os.ReadFile(h.SessionFile())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHandoffRidesTheFirstPromptOfAFreshSessionOnce(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	start(t, h, "t1", input(1, "say hello"))
	first := lastPrompt(t, r)
	if !strings.Contains(first, "<user_request>\nEARLIER CONTEXT\n\nUser message:\nsay hello\n</user_request>") {
		t.Fatalf("first prompt = %s", first)
	}
	start(t, h, "t2", input(2, "say again"))
	if strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT") {
		t.Fatal("the handoff was sent twice")
	}
	// The mirror holds the user's own words, with the entry that carried the handoff marked.
	mirror := mirrorText(t, h)
	if strings.Contains(mirror, "EARLIER CONTEXT") || strings.Count(mirror, `"type":"handoff"`) != 1 {
		t.Fatalf("mirror = %s", mirror)
	}
	meta, _ := readMeta(c.SessionDir)
	if meta.Handoff != "" {
		t.Fatalf("meta keeps %q", meta.Handoff)
	}
}

func TestHandoffWithNoUserTextContinues(t *testing.T) {
	h, r, _ := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	if _, err := h.Continue(context.Background(), "t1", func() {}); err != nil {
		t.Fatal(err)
	}
	if got := lastPrompt(t, r); !strings.Contains(got, "EARLIER CONTEXT\n\nUser message:\nContinue where you left off.") {
		t.Fatalf("prompt = %s", got)
	}
}

func TestHandoffIsIgnoredWhenTheStoredSessionResumes(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	c.New, c.Handoff = false, "EARLIER CONTEXT"
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say again"))
	if strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT") {
		t.Fatal("a resumed session was handed a history")
	}
}

func TestSlashCommandIsNotGivenTheHandoffAndTheNextPromptIs(t *testing.T) {
	h, r, _ := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	start(t, h, "t1", input(1, "/help"))
	if strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT") {
		t.Fatal("a slash command carries the handoff")
	}
	start(t, h, "t2", input(2, "say hello"))
	if !strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT\n\nUser message:\nsay hello") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
}

func TestHandoffWaitsInTheSessionFileUntilAgyAcceptsAPrompt(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	h.Close()
	// A restart with no handoff in its config (the engine's flag was spent) still delivers it, in the session it opened.
	c.New, c.Handoff = false, ""
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t1", input(1, "say hello"))
	if !strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT\n\nUser message:\nsay hello") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
	if n := len(r.calls(t, "session/new")); n != 1 {
		t.Fatalf("session/new calls = %d", n)
	}
}

func TestACrashAfterAcceptNeverResendsTheHandoff(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	// The same boot again: the engine asks for a new conversation with the same handoff.
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say again"))
	if strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT") {
		t.Fatal("the handoff was sent again")
	}
	if n := len(r.calls(t, "session/new")); n != 1 || h2.SessionFile() != filepath.Join(c.SessionDir, "agy-1.jsonl") {
		t.Fatalf("a new session was opened: %d, %s", n, h2.SessionFile())
	}
}

func TestSessionAgyCannotResumeStartsFreshWithAHandoffOfTheMirror(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say the first words"))
	h.Close()
	t.Setenv("FAKE_AGY_MISSING", "fake-session-1")
	c.New = false
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	select {
	case n := <-r.notices:
		if !strings.Contains(n, "could not be resumed") {
			t.Fatalf("notice = %q", n)
		}
	default:
		t.Fatal("no notice")
	}
	start(t, h2, "t2", input(2, "say more"))
	got := lastPrompt(t, r)
	for _, want := range []string{"Provider context handoff", "[Historical user; user_message; file=agy-1.jsonl;", "say the first words", "[Historical assistant; assistant_message;", "User message:\nsay more"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt lacks %q: %s", want, got)
		}
	}
	if h2.SessionFile() != filepath.Join(c.SessionDir, "agy-2.jsonl") {
		t.Fatalf("file = %s", h2.SessionFile())
	}
}

func TestResumeWithoutTheCapabilityStartsFreshWithAHandoff(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say the first words"))
	h.Close()
	t.Setenv("FAKE_AGY_NO_RESUME", "1")
	c.New = false
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say more"))
	if got := lastPrompt(t, r); !strings.Contains(got, "say the first words") {
		t.Fatalf("prompt = %s", got)
	}
}

func TestResumeFailureUsesTheHandoffTheCallerGave(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say the first words"))
	h.Close()
	t.Setenv("FAKE_AGY_MISSING", "fake-session-1")
	c.New, c.Handoff = false, "FROM THE CALLER"
	// Handoff applies to a fresh session: this one is fresh because agy could not resume.
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say more"))
	got := lastPrompt(t, r)
	if strings.Contains(got, "[Historical") || !strings.Contains(got, "FROM THE CALLER\n\nUser message:\nsay more") {
		t.Fatalf("prompt = %s", got)
	}
}

func TestSignInErrorOnResumeIsNotAHandoff(t *testing.T) {
	h, _, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	t.Setenv("FAKE_AGY_AUTH_FAIL", "1")
	c.New = false
	if _, err := Open(context.Background(), c); err == nil {
		t.Fatal("a sign-in failure opened a session")
	}
}

func TestANewGenerationStartsAFreshSessionWithTheNewHistory(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "FIRST HISTORY", "1" })
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	// The same generation replayed is that session: no second one.
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	h2.Close()
	if n := len(r.calls(t, "session/new")); n != 1 {
		t.Fatalf("the same generation opened session %d", n)
	}
	// A new one starts a fresh native session with its own history.
	c.Handoff, c.HandoffGen = "SECOND HISTORY", "2"
	h3, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h3.Close()
	start(t, h3, "t2", input(2, "say again"))
	got := lastPrompt(t, r)
	if n := len(r.calls(t, "session/new")); n != 2 || !strings.Contains(got, "SECOND HISTORY\n\nUser message:\nsay again") {
		t.Fatalf("sessions %d, prompt = %s", n, got)
	}
	meta, _ := readMeta(c.SessionDir)
	if meta.Gen != "2" || meta.Handoff != "" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestClearDropsAHandoffNotYetDeliveredAndTheSameGenerationDoesNotBringItBack(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "EARLIER CONTEXT", "7" })
	if err := h.NewConversation(context.Background()); err != nil {
		t.Fatal(err)
	}
	start(t, h, "t1", input(1, "say hello"))
	if strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT") {
		t.Fatal("/clear left the handoff pending")
	}
	h.Close()
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say again"))
	if strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT") {
		t.Fatal("the same generation brought the handoff back")
	}
}

func TestAPathIsNotASlashCommand(t *testing.T) {
	h, r, _ := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	start(t, h, "t1", input(1, "/box/work/dyna is the repo"))
	if !strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT\n\nUser message:\n/box/work/dyna is the repo") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
	for text, want := range map[string]bool{"/help": true, "  /compact now": true, "/box/work": false, "hello /x": false, "/": false} {
		if slashCommand.MatchString(text) != want {
			t.Errorf("slash(%q) != %v", text, want)
		}
	}
}

func TestAPromptAgyRefusesAsInvalidKeepsTheHandoffPending(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	if _, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "rejectprompt")}, nil); err == nil {
		t.Fatal("the refusal was not reported")
	}
	meta, _ := readMeta(c.SessionDir)
	if meta.Status != handoffPending || meta.Handoff == "" || handoffDelivered(h.SessionFile(), meta.Gen, "engine") {
		t.Fatalf("meta = %+v: the refused prompt consumed the handoff", meta)
	}
	start(t, h, "t2", input(2, "say hello"))
	if !strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT\n\nUser message:\nsay hello") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
}

func TestAHandoffWhoseDeliveryIsUncertainIsGivenToAFreshSession(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	res, _ := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "crash")}, nil)
	_ = res
	h.Close()
	meta, _ := readMeta(c.SessionDir)
	if meta.Handoff == "" || meta.Status != handoffSending {
		t.Fatalf("meta = %+v: an unanswered prompt is not marked as being sent", meta)
	}
	// The next run cannot know whether agy took it: a fresh native session gets it again (T3 ContextHandoffDeliveryUncertainError).
	c.New = false
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say hello"))
	if n := len(r.calls(t, "session/new")); n != 2 || !strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT\n\nUser message:\nsay hello") {
		t.Fatalf("sessions %d, prompt = %s", n, lastPrompt(t, r))
	}
}

func TestAResumeRefusedForAnAccountFailureFailsOnce(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	t.Setenv("FAKE_AGY_RESUME_RESTRICTION", "1")
	c.New = false
	if next, err := Open(context.Background(), c); err == nil || len(r.calls(t, "session/new")) != 1 {
		if next != nil {
			next.Close()
		}
		t.Fatalf("open = %v, session/new calls = %d", err, len(r.calls(t, "session/new")))
	}
}

func TestASteerCannotConsumeTheHandoffAnotherPromptCarries(t *testing.T) {
	dir := t.TempDir()
	read, write := io.Pipe()
	defer read.Close()
	defer write.Close()
	var sent bytes.Buffer
	p := &process{client: acp.NewClient(read, &sent, acp.Handler{})}
	tn := &turn{id: "t", open: 2, wake: make(chan struct{}, 1), queued: make(chan struct{})}
	var warnings atomic.Int32
	paused, release := make(chan struct{}), make(chan struct{})
	h := &Harness{p: p, turn: tn, sessionID: "native", mirror: filepath.Join(dir, "agy-1.jsonl"), handoff: "PENDING_HISTORY", hstatus: handoffPending, cfg: Config{GeminiDir: dir, SessionDir: dir, Notice: func(string) {
		if warnings.Add(1) == 1 {
			close(paused)
			<-release
		}
	}}}
	result := make(chan error, 1)
	go func() { result <- h.begin(tn, []contract.TurnInput{input(1, "normal follow up")}, nil, false) }()
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the follow-up did not reach the pause")
	}
	// The carrying prompt is built and not yet sent; a steer and a second prompt meanwhile carry nothing and settle nothing.
	err := h.begin(tn, []contract.TurnInput{input(2, "interruption")}, nil, true)
	err2 := h.begin(tn, []contract.TurnInput{input(3, "another")}, nil, false)
	before := sent.String()
	h.mu.Lock()
	still := h.handoff
	h.mu.Unlock()
	close(release)
	if err != nil || err2 != nil || <-result != nil {
		t.Fatal(err, err2)
	}
	if strings.Contains(before, "PENDING_HISTORY") || still != "PENDING_HISTORY" {
		t.Fatalf("the history was sent early or consumed: %q, %q", before, still)
	}
	if n := strings.Count(sent.String(), "PENDING_HISTORY"); n != 1 {
		t.Fatalf("the history was sent %d times", n)
	}
}

func TestAnExplicitNewGenerationWinsOverAnUncertainOldOne(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "OLD HISTORY", "1" })
	h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "crash")}, nil)
	h.Close()
	c.Handoff, c.HandoffGen = "NEW HISTORY", "2"
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t2", input(2, "say hello"))
	got := lastPrompt(t, r)
	if !strings.Contains(got, "NEW HISTORY\n\nUser message:\nsay hello") || strings.Contains(got, "OLD HISTORY") {
		t.Fatalf("prompt = %s", got)
	}
}

func TestAClearedGenerationIsNeverSuppliedAgain(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "CLEARED HISTORY", "7" })
	if err := h.NewConversation(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.Close()
	// The same generation replayed, and agy cannot resume the session: nothing brings the cleared history back.
	t.Setenv("FAKE_AGY_MISSING", "fake-session-1")
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t1", input(1, "say hello"))
	if strings.Contains(lastPrompt(t, r), "CLEARED HISTORY") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
}

func TestAnExplicitGenerationWithNoHistoryStillStandsForItsSession(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "", "9" })
	start(t, h, "t1", input(1, "say hello"))
	h.Close()
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if n := len(r.calls(t, "session/new")); n != 1 {
		t.Fatalf("the same empty generation opened session %d", n)
	}
}

func TestAHandoffMarkThatCannotBeSavedStopsTheSend(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff = "EARLIER CONTEXT" })
	// The session file is written through a temporary file; a directory in its place makes every save fail.
	if err := os.Mkdir(filepath.Join(c.SessionDir, "agy-session.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(context.Background(), "t1", []contract.TurnInput{input(1, "say hello")}, nil); err == nil {
		t.Fatal("the prompt was sent without its handoff mark being saved")
	}
	if n := len(r.calls(t, "session/prompt")); n != 0 {
		t.Fatalf("%d prompts reached agy", n)
	}
	// The handoff is pending again, for the next prompt.
	os.Remove(filepath.Join(c.SessionDir, "agy-session.json.tmp"))
	start(t, h, "t2", input(2, "say hello"))
	if !strings.Contains(lastPrompt(t, r), "EARLIER CONTEXT\n\nUser message:\nsay hello") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
}

func TestAHandoffAfterAClearReadsOnlyWhatCameAfterIt(t *testing.T) {
	h, r, c := open(t, nil, nil)
	start(t, h, "t1", input(1, "say before the clear"))
	if err := h.NewConversation(context.Background()); err != nil {
		t.Fatal(err)
	}
	start(t, h, "t2", input(2, "say after the clear"))
	h.Close()
	t.Setenv("FAKE_AGY_MISSING", "fake-session-2")
	c.New = false
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	start(t, h2, "t3", input(3, "say more"))
	got := lastPrompt(t, r)
	if !strings.Contains(got, "say after the clear") || strings.Contains(got, "say before the clear") {
		t.Fatalf("prompt = %s", got)
	}
	// The earlier files are still there for the transcript.
	if _, err := os.Stat(filepath.Join(c.SessionDir, "agy-1.jsonl")); err != nil {
		t.Fatal(err)
	}
}

func TestALocalFallbackNeverChangesTheEnginesGeneration(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "ORIGINAL HISTORY", "7" })
	start(t, h, "t1", input(1, "say the first words"))
	h.Close()
	// The same hello is replayed and agy cannot resume: the history is rebuilt from the mirror, as a record of its own.
	t.Setenv("FAKE_AGY_MISSING", "fake-session-1")
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	h2.Close()
	meta, _ := readMeta(c.SessionDir)
	if meta.Gen != "7" || meta.Status != handoffAccepted || meta.Fallback == "" || meta.FallbackStatus != handoffPending {
		t.Fatalf("meta = %+v", meta)
	}
	// A restart before the next turn, with the same hello once more: no third native conversation, no original history.
	t.Setenv("FAKE_AGY_MISSING", "") // the fake names its sessions from 1 in every process, so the replacement has the refused id
	h3, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h3.Close()
	start(t, h3, "t2", input(2, "say more"))
	got := lastPrompt(t, r)
	if n := len(r.calls(t, "session/new")); n != 2 || strings.Contains(got, "ORIGINAL HISTORY") || !strings.Contains(got, "say the first words\n") && !strings.Contains(got, "say the first words") {
		t.Fatalf("sessions %d, prompt = %s", n, got)
	}
	meta, _ = readMeta(c.SessionDir)
	if meta.Gen != "7" || meta.FallbackStatus != handoffAccepted {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestAClearedGenerationStaysClearedAcrossAFallbackAndARestart(t *testing.T) {
	h, r, c := open(t, nil, func(c *Config) { c.Handoff, c.HandoffGen = "CLEARED HISTORY", "7" })
	if err := h.NewConversation(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.Close()
	t.Setenv("FAKE_AGY_MISSING", "fake-session-1")
	h2, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	h2.Close()
	t.Setenv("FAKE_AGY_MISSING", "")
	h3, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h3.Close()
	start(t, h3, "t1", input(1, "say hello"))
	if strings.Contains(lastPrompt(t, r), "CLEARED HISTORY") {
		t.Fatalf("prompt = %s", lastPrompt(t, r))
	}
	if meta, _ := readMeta(c.SessionDir); meta.Gen != "7" {
		t.Fatalf("meta = %+v", meta)
	}
}
