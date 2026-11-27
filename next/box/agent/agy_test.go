package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy/fakeagent"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeagent.EnvVar) != "" {
		fakeagent.Run(os.Stdin, os.Stdout)
		return
	}
	os.Exit(m.Run())
}

func TestOpenAgyNeedsItsFiles(t *testing.T) {
	_, err := openAgy(t.Context(), contract.BoxHelloResponse{Harness: contract.HarnessAgy}, "127.0.0.1:7782", "/box/work", t.TempDir(), nil, &activity{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "without its files") {
		t.Fatal(err)
	}
}

// Both room instruction files reach agy's system prompt append exactly once, and nothing is written into the profile folder.
func TestAgySessionGetsTheRoomInstructionsOnce(t *testing.T) {
	dir := t.TempDir()
	global, room := filepath.Join(dir, "global.md"), filepath.Join(dir, "room.md")
	if err := os.WriteFile(global, []byte("GLOBAL RULE"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(room, []byte("ROOM RULE"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "agent.log")
	t.Setenv(fakeagent.EnvVar, "1")
	t.Setenv("FAKE_AGY_LOG", log)
	profile := t.TempDir()
	hello := contract.BoxHelloResponse{Harness: contract.HarnessAgy, Instructions: "be Dyna", Fresh: true, Agy: &contract.BoxAgy{Binary: exe, GeminiDir: profile, Exit: contract.AgyExitPath}, Model: contract.ModelChoice{ID: "gemini-3.8-flash", Thinking: contract.ThinkingLow}}
	h, err := openAgy(t.Context(), hello, "127.0.0.1:7782", t.TempDir(), t.TempDir(), []string{global, room, filepath.Join(dir, "missing.md")}, &activity{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	turn := []contract.TurnInput{{Text: "say hi"}}
	for _, id := range []contract.TurnID{"t1", "t2"} {
		if _, err := h.Start(t.Context(), id, turn, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(log)
	var prompts []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		var m struct {
			Method string `json:"method"`
			Params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &m) == nil && m.Method == "session/prompt" {
			var text string
			for _, b := range m.Params.Prompt {
				text += b.Text
			}
			prompts = append(prompts, text)
		}
	}
	if len(prompts) != 2 || strings.Contains(prompts[1], "RULE") {
		t.Fatalf("the rules ride on the first prompt only: %q", prompts)
	}
	appended := prompts[0]
	for _, want := range []string{"be Dyna", "GLOBAL RULE", "ROOM RULE", `<project_instructions path="` + global + `">`} {
		if strings.Count(appended, want) != 1 {
			t.Fatalf("%q appears %d times in %q", want, strings.Count(appended, want), appended)
		}
	}
	if _, err := os.Stat(filepath.Join(profile, "AGENTS.md")); err == nil {
		t.Fatal("an instruction file was written into the profile folder")
	}
}

// The retry wake of a quota failure replays the same delivery. It must not report success from the error text agy mirrored as its
// answer: the turn runs again with one new prompt, "Continue where you left off.", and only that answer completes it.
func TestARetryWakeAfterAQuotaFailureSendsOneContinuePrompt(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "agent.log")
	t.Setenv(fakeagent.EnvVar, "1")
	t.Setenv("FAKE_AGY_LOG", log)
	hello := contract.BoxHelloResponse{Harness: contract.HarnessAgy, Fresh: true, Agy: &contract.BoxAgy{Binary: exe, GeminiDir: t.TempDir(), Exit: contract.AgyExitPath}, Model: contract.ModelChoice{ID: "gemini-3.8-flash", Thinking: contract.ThinkingLow}}
	h, err := openAgy(t.Context(), hello, "127.0.0.1:7782", t.TempDir(), t.TempDir(), nil, &activity{}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	sink := &sinkFake{acked: make(map[contract.DeliverySeq]bool), done: make(chan contract.TurnEnded, 4)}
	runner := &Runner{Harness: h, Sink: sink}
	start := func(turn contract.TurnID) contract.TurnEnded {
		t.Helper()
		message := contract.BoxMessage{Body: contract.TurnStart{Turn: turn, Inputs: []contract.TurnInput{{Delivery: 1, Text: "quota"}}}}
		if err := runner.Handle(t.Context(), message); err != nil {
			t.Fatal(err)
		}
		return <-sink.done
	}
	if first := start("t1"); first.Outcome != contract.TurnFailed || first.Cause != contract.CauseModelFailed || first.ModelRetryAfterSeconds == 0 {
		t.Fatalf("the quota failure ended as %+v", first)
	}
	retry := start("t2")
	if retry.Outcome != contract.TurnCompleted {
		t.Fatalf("the retry ended as %+v", retry)
	}
	data, _ := os.ReadFile(log)
	if n := strings.Count(string(data), "Continue where you left off."); n != 1 {
		t.Fatalf("%d continue prompts after the retry wake, want 1", n)
	}
}

func TestOpenAgyRefusesWithoutAnExit(t *testing.T) {
	for name, c := range map[string]struct {
		exit, proxy string
	}{"engine without an exit": {"", "127.0.0.1:7782"}, "no listener": {"/agy-exit/", ""}} {
		hello := contract.BoxHelloResponse{Harness: contract.HarnessAgy, Agy: &contract.BoxAgy{Binary: "/bin/true", GeminiDir: t.TempDir(), Exit: c.exit}}
		if _, err := openAgy(t.Context(), hello, c.proxy, "/box/work", t.TempDir(), nil, &activity{}, func(string) {}); err == nil || !strings.Contains(err.Error(), "agy is not started") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
