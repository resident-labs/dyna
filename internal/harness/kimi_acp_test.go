package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestKimiACPStartupTimeoutReplacesChildOnResume(t *testing.T) {
	dir := acpScript(t, map[string]string{
		"stalled": `{"waitForCancel":true}`,
		"resume":  `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"current answer"}}}|{"stopReason":"end_turn"}`,
	})
	history := `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old answer"}}}` + "\n" +
		`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"old-tool","status":"completed"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "load.jsonl"), []byte(history), 0o600); err != nil {
		t.Fatal(err)
	}
	env, mark := acpTestEnv(t, dir)
	p := kimiACPProfile(env)
	cwd := t.TempDir()
	const sessionID = "fake-session-1"
	fired := make(chan time.Time)
	go func() {
		waitForMark(t, mark, "prompt stalled")
		close(fired)
	}()
	_, err := RunWithOptions(t.Context(), p, "stalled", cwd, false, JournalOptions{}, SteeringOptions{},
		SessionOptions{ResumeID: sessionID}, ExecutionOptions{HealthProbe: true, startupFired: fired})
	if err == nil || !strings.Contains(err.Error(), "failed to start") {
		t.Fatalf("startup timeout = %v", err)
	}
	result, err := RunWithOptions(t.Context(), p, "resume", cwd, false, JournalOptions{}, SteeringOptions{},
		SessionOptions{ResumeID: sessionID}, ExecutionOptions{HealthProbe: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != sessionID || result.Output != "current answer" {
		t.Fatalf("resumed session = %q, output = %q", result.SessionID, result.Output)
	}
	body, err := os.ReadFile(mark)
	if err != nil {
		t.Fatal(err)
	}
	if boots := strings.Count(string(body), "boot "); boots != 2 {
		t.Fatalf("startup timeout retained the stalled child: %d boots; want 2\n%s", boots, body)
	}
	if loads := strings.Count(string(body), "session/load "+sessionID); loads != 2 {
		t.Fatalf("session was not loaded in both children: %d loads\n%s", loads, body)
	}
}

func kimiACPProfile(env map[string]string) profile.Profile {
	p := acpProfile(env)
	p.Name = "kimi-fixture"
	p.Harness = profile.HarnessKimiCode
	p.Env["KIMI_CODE_HOME"] = p.Env["GROK_HOME"]
	p.Env["FAKE_KIMI"] = "1"
	return p
}

func TestKimiACPSelectsModelAndThinking(t *testing.T) {
	dir := acpScript(t, map[string]string{
		"task": `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}}|{"stopReason":"end_turn"}`,
	})
	env, mark := acpTestEnv(t, dir)
	p := kimiACPProfile(env)
	p.Model = "kimi-code/k3#high"
	zero := time.Duration(0)
	result, err := RunWithOptions(t.Context(), p, "task", t.TempDir(), true, JournalOptions{}, SteeringOptions{}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true, startupLimit: time.Second})
	if err != nil {
		body, _ := os.ReadFile(mark)
		t.Fatalf("%v\n%s", err, body)
	}
	if result.Output != "answer" {
		t.Fatalf("output = %q", result.Output)
	}
	body, _ := os.ReadFile(mark)
	text := string(body)
	if !strings.Contains(text, "model kimi-code/k3\n") || strings.Contains(text, "model kimi-code/k3#high") {
		t.Fatalf("model selection did not use the bare alias:\n%s", text)
	}
	if !strings.Contains(text, "config thinking high") {
		t.Fatalf("thinking selection is missing:\n%s", text)
	}
	if !strings.Contains(text, "mode yolo") {
		t.Fatalf("headless permission mode is missing:\n%s", text)
	}
}

func TestKimiAcpSteeringInterruptsAndResumes(t *testing.T) {
	steered := steeringPrompt([]runstore.SteeringMessage{{Message: "new evidence"}})
	dir := acpScript(t, map[string]string{
		"long task": `{"waitForCancel":true}`,
		steered:     `{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"STEERED"}}}|{"stopReason":"end_turn"}`,
	})
	env, mark := acpTestEnv(t, dir)
	p := kimiACPProfile(env)
	source := &doorbellSource{wake: make(chan struct{}), observed: make(chan struct{}, 1)}
	go func() {
		waitForMark(t, mark, "prompt long task")
		source.ring(runstore.SteeringMessage{Message: "new evidence"})
	}()
	zero := time.Duration(0)
	result, err := RunWithOptions(t.Context(), p, "long task", t.TempDir(), true, JournalOptions{}, SteeringOptions{Source: source, pollEvery: time.Hour}, SessionOptions{RecoveryDelay: &zero}, ExecutionOptions{HealthProbe: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "STEERED" {
		t.Fatalf("output = %q", result.Output)
	}
	body, _ := os.ReadFile(mark)
	text := string(body)
	if !strings.Contains(text, "cancel") || !strings.Contains(text, "session/load fake-session-1") {
		t.Fatalf("steering did not interrupt and restore the Kimi session:\n%s", text)
	}
	if strings.Contains(text, "busy") {
		t.Fatalf("steering used a concurrent Kimi prompt:\n%s", text)
	}
}
