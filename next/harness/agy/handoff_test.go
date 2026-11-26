package agy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if strings.Contains(mirror, "EARLIER CONTEXT") || strings.Count(mirror, `"handoff":true`) != 1 {
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
