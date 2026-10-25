package harness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
)

func TestKimiACPReportsHiddenFailure(t *testing.T) {
	for _, code := range []string{"provider.connection_error", "loop.max_steps_exceeded"} {
		t.Run(code, func(t *testing.T) {
			dir := acpScript(t, map[string]string{
				"task": `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Work in progress."}}}|{"kimiTurnEnd":{"type":"turn.ended","agentId":"main","reason":"failed","error":{"code":"` + code + `","message":"Connection error."}}}|{"stopReason":"end_turn"}`,
			})
			env, _ := acpTestEnv(t, dir)
			p := kimiACPProfile(env)
			result, err := RunWithOptions(t.Context(), p, "task", t.TempDir(), false, JournalOptions{}, SteeringOptions{}, SessionOptions{}, ExecutionOptions{HealthProbe: true})
			var failure *kimiTurnError
			if !errors.As(err, &failure) || failure.Code != code {
				t.Fatalf("hidden failure = %v; want typed %s", err, code)
			}
			if result.Output != "Work in progress." {
				t.Fatalf("partial work was lost: %q", result.Output)
			}
		})
	}
}

func TestKimiPromptJournalIgnoresOldAndUnrelatedFailures(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "sessions", "workspace", "session-current", "agents", "main", "wire.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	failure := `{"type":"turn.ended","agentId":"main","reason":"failed","error":{"code":"provider.connection_error","message":"Connection error."}}` + "\n"
	if err := os.WriteFile(path, []byte(failure), 0600); err != nil {
		t.Fatal(err)
	}
	journal := newKimiPromptJournal(home, "session-current")
	if err := journal.promptError(); err != nil {
		t.Fatalf("old failure leaked into this prompt: %v", err)
	}
	other := filepath.Join(home, "sessions", "workspace", "session-other", "agents", "main", "wire.jsonl")
	if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(failure), 0600); err != nil {
		t.Fatal(err)
	}
	// A long tool result may precede the terminal record in the bounded tail.
	body := failure + `{"type":"tool.result","output":"` + strings.Repeat("x", 100000) + `"}` + "\n" +
		`{"type":"turn.ended","agentId":"child","reason":"failed","error":{"code":"tool.failed","message":"child failed"}}` + "\n" +
		`{"type":"turn.ended","agentId":"main","reason":"completed"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := journal.promptError(); err != nil {
		t.Fatalf("unrelated failure became a prompt error: %v", err)
	}
	if err := acpStopReasonError(profile.HarnessKimiCode, "max_turn_requests"); err == nil || !strings.Contains(err.Error(), "step limit") {
		t.Fatalf("step-limit stop reason = %v", err)
	}
}
