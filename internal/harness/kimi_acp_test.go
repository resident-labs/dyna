package harness

import (
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

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
