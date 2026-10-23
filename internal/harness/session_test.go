package harness

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestClaudeStreamJSONInvocationAndResume(t *testing.T) {
	p := profile.Profile{
		Name: "c", Harness: profile.HarnessClaudeCode,
		Output: &workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON},
	}
	inv, err := buildInvocation(p, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("sid", "go on")} {
		if !hasOptionValue(spec.argv, "--output-format", "stream-json") || !hasOptionValue(spec.argv, "--input-format", "stream-json") ||
			!hasArg(spec.argv, "--verbose") || !hasArg(spec.argv, "--replay-user-messages") || spec.parseOutput == nil || !spec.streamInput {
			t.Fatalf("claude stream-json argv = %v", spec.argv)
		}
	}
	got := inv.initial.parseOutput(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n" +
		`{"type":"result","result":"final text","session_id":"s"}`)
	if got != "final text" {
		t.Fatalf("parsed output = %q", got)
	}
	p.ExtraArgs = []string{"--output-format", "json"}
	if _, err := buildInvocation(p, "task"); err == nil {
		t.Fatal("conflicting --output-format was accepted")
	}
	// Old profiles use the native stream format without a declaration.
	plain, _ := buildInvocation(profile.Profile{Harness: profile.HarnessClaudeCode}, "task")
	if !hasOptionValue(plain.initial.argv, "--output-format", "stream-json") || !plain.initial.streamInput {
		t.Fatalf("default claude argv lacks stream-json: %v", plain.initial.argv)
	}
}

func TestResumableReflectsHarnessSessionSupport(t *testing.T) {
	for p, want := range map[string]bool{
		profile.HarnessClaudeCode: true, profile.HarnessCodex: true, profile.HarnessMock: true,
		profile.HarnessCustom: false,
	} {
		prof := profile.Profile{Harness: p, Command: []string{"x"}}
		if got := Resumable(prof); got != want {
			t.Errorf("Resumable(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestRunSessionTurnRefusesToResumeNonResumableHarness(t *testing.T) {
	p := profile.Profile{Name: "custom", Harness: profile.HarnessCustom, Command: []string{"echo", "{{prompt}}"}}
	_, err := RunSessionTurn(context.Background(), p, "x", t.TempDir(), true, JournalOptions{}, SteeringOptions{},
		SessionOptions{ResumeID: "abc"})
	if err == nil || !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("err = %v, want a clear cannot-resume error", err)
	}
}

func TestMockSessionResumesAndIsSteered(t *testing.T) {
	var id string
	mb := &testSource{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan Result, 1)
	// OnSession reports the turn's session id once it starts, so the test
	// steers a turn that is running instead of waiting out a timer.
	started := make(chan struct{})
	go func() {
		r, _ := RunSessionTurn(ctx, profile.Profile{Name: "m", Harness: profile.HarnessMock}, "work SLEEP:2000", t.TempDir(), true,
			JournalOptions{}, SteeringOptions{Source: mb}, SessionOptions{OnSession: func(s string) { id = s; close(started) }})
		done <- r
	}()
	select {
	case <-started:
	case r := <-done:
		t.Fatalf("mock turn ended before it could be steered: %#v", r)
	}
	mb.submit("go left")
	var r Result
	select {
	case r = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("mock turn ignored steering")
	}
	if !strings.Contains(r.Output, "steered: go left") || r.SessionID == "" || r.SessionID != id {
		t.Fatalf("steered mock turn = %#v (session %q)", r, id)
	}
	again, err := RunSessionTurn(ctx, profile.Profile{Name: "m", Harness: profile.HarnessMock}, "next", t.TempDir(), true,
		JournalOptions{}, SteeringOptions{Source: &testSource{}}, SessionOptions{ResumeID: id})
	if err != nil || again.SessionID != id {
		t.Fatalf("resumed mock turn = %#v, %v", again, err)
	}
}

func TestAntigravityDiscoveredModelUsesACP(t *testing.T) {
	p := profile.Profile{Harness: profile.HarnessAntigravity, Model: "gemini-discovered-alternate"}
	inv, err := buildInvocation(p, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("conversation", "continue")} {
		if len(spec.argv) != 0 || spec.acpPrompt == "" {
			t.Fatalf("Antigravity model must use ACP session/set_model, got %+v", spec)
		}
	}
}

func TestAntigravityNativeDefaultDoesNotPassEmptyModel(t *testing.T) {
	inv, err := buildInvocation(profile.Profile{Harness: profile.HarnessAntigravity}, "task")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []commandSpec{inv.initial, inv.resume("conversation", "continue")} {
		if hasArg(spec.argv, "--model") {
			t.Fatalf("default model was overridden: %v", spec.argv)
		}
	}
}
