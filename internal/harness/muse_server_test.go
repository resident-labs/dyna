package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
)

// The fake server retains protocol state across attachments, just like serve.
type fakeMuseHost struct {
	mu      sync.Mutex
	alive   bool
	starts  int
	calls   []string
	frames  chan []byte
	session string
	hold    bool
}

func (h *fakeMuseHost) Session(_ context.Context, launch sessionhost.Launch) (sessionhost.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.alive {
		h.alive = true
		h.starts++
		if launch.OnStart != nil {
			launch.OnStart(123)
		}
	}
	for _, arg := range launch.Argv {
		if arg == "--yolo" || arg == "--no-foreign-personal-context" || arg == "--reasoning-effort" || arg == "--permission-profile" || arg == "--approval-mode" {
			return nil, fmt.Errorf("exec-only flag: %s", arg)
		}
	}
	h.frames = make(chan []byte, 32)
	return h, nil
}
func (h *fakeMuseHost) Write(frame []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var r struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	if err := json.Unmarshal(frame, &r); err != nil {
		return err
	}
	if r.Method == "" {
		return nil
	}
	h.calls = append(h.calls, r.Method)
	if r.Method != "initialize" && r.Method != "initialized" {
		id, err := uuid.Parse(fmt.Sprint(r.Params["commandId"]))
		if err != nil || id.Version() != 7 {
			return fmt.Errorf("command needs UUIDv7")
		}
	}
	emit := func(v any) { b, _ := json.Marshal(v); h.frames <- b }
	reply := func(v any) { emit(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": v}) }
	notify := func(method string, p any) { emit(map[string]any{"jsonrpc": "2.0", "method": method, "params": p}) }
	finish := func(terminal string) {
		notify("session/tokenUsage", map[string]any{"sessionId": h.session, "turnId": "turn-1", "promptTokens": 10, "usage": map[string]int{"inputTokens": 10, "cachedTokens": 2, "outputTokens": 5}})
		notify("session/contextUsage", map[string]any{"sessionId": h.session, "usedTokens": 42})
		notify("item/completed", map[string]any{"sessionId": h.session, "turnId": "turn-1", "item": map[string]any{"itemId": "answer", "turnId": "turn-1", "kind": "agentMessage", "status": "completed", "text": "done"}})
		notify("turn/completed", map[string]any{"sessionId": h.session, "turnId": "turn-1", "terminal": terminal})
	}
	switch r.Method {
	case "initialize":
		reply(map[string]any{"schema": map[string]any{"fingerprint": "fake", "version": 1}})
	case "initialized":
	case "session/start", "session/resume":
		if r.Params["permissionProfile"] != ":unrestricted" || r.Params["approvalMode"] != "allowAll" {
			return fmt.Errorf("lost explicit session permission policy")
		}
		if r.Params["commandId"] == nil {
			return fmt.Errorf("missing commandId")
		}
		h.session, _ = r.Params["sessionId"].(string)
		if h.session == "" {
			h.session = "session-1"
		}
		reply(map[string]any{"session": map[string]any{"sessionId": h.session}})
	case "turn/start":
		if r.Params["sessionId"] != h.session || r.Params["commandId"] == nil {
			return fmt.Errorf("wrong command identity")
		}
		reply(map[string]any{"turnId": "turn-1"})
		notify("item/started", map[string]any{"sessionId": h.session, "turnId": "turn-1", "item": map[string]any{"itemId": "thinking", "turnId": "turn-1", "kind": "reasoning", "status": "inProgress"}})
		if !h.hold {
			approval := map[string]any{"sessionId": h.session, "approvalId": "approval-1", "currentRequirementId": map[string]any{"approvalId": "approval-1", "sourceIndex": 1}, "availableChoices": []any{map[string]string{"choiceId": "deny", "decision": "denied"}}}
			emit(map[string]any{"jsonrpc": "2.0", "id": "approval-presentation", "method": "approval/request", "params": approval})
			notify("approval/requested", approval)
			notify("approval/updated", approval)
		}
	case "approval/decide":
		if r.Params["choiceId"] != "deny" {
			return fmt.Errorf("approval widened")
		}
		reply(map[string]any{})
		notify("userInput/requested", map[string]any{"sessionId": h.session, "userInputId": "input-1"})
	case "userInput/cancel":
		reply(map[string]any{})
		finish("completed")
	case "turn/steer":
		if r.Params["expectedTurnId"] != "turn-1" {
			return fmt.Errorf("steered wrong turn")
		}
		reply(map[string]any{"turnId": "turn-1"})
		finish("completed")
	case "turn/interrupt":
		if r.Params["turnId"] != "turn-1" {
			return fmt.Errorf("interrupted wrong turn")
		}
		reply(map[string]any{})
		finish("cancelled")

	default:
		reply(map[string]any{})
	}
	return nil
}
func (h *fakeMuseHost) Next(ctx context.Context) ([]byte, error) {
	select {
	case b := <-h.frames:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (h *fakeMuseHost) Replace(context.Context, sessionhost.Launch) error { return io.ErrUnexpectedEOF }
func (h *fakeMuseHost) Detach(sessionhost.DetachReason)                   {}
func (h *fakeMuseHost) Close() error                                      { h.mu.Lock(); h.alive = false; h.mu.Unlock(); return nil }

func TestMuseServeReusesHostAndResumesAfterReplacement(t *testing.T) {
	host := &fakeMuseHost{}
	old := nativeSessionHost
	nativeSessionHost = host
	t.Cleanup(func() { nativeSessionHost = old })
	p := profile.Profile{Harness: profile.HarnessMuse, RuntimeExecutable: "/bin/false", ExtraArgs: []string{"--yolo", "--reasoning-effort", "high"}}
	normalized := t.TempDir() + "/normalized.jsonl"
	if err := os.WriteFile(normalized, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	var id string
	run := func(resume string) {
		t.Helper()
		got, err := RunWithOptions(t.Context(), p, "task", cwd, false, JournalOptions{NormalizedOutputPath: normalized}, SteeringOptions{}, SessionOptions{ResumeID: resume, OnSession: func(s string) { id = s }}, ExecutionOptions{SessionKey: "muse-test"})
		if err != nil || got.Output != "done" {
			t.Fatalf("turn: %+v, %v", got, err)
		}
		if got.ContextTokens != 42 {
			t.Fatalf("lost usage: %+v", got)
		}
	}
	run("")
	run(id)
	if host.starts != 1 {
		t.Fatalf("started %d processes", host.starts)
	}
	host.Close()
	run(id)
	host.hold = true
	_, err := RunWithOptions(t.Context(), p, "steer", cwd, false, JournalOptions{}, SteeringOptions{Source: &armedSource{armed: true, message: "finish"}}, SessionOptions{ResumeID: id}, ExecutionOptions{SessionKey: "muse-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = RunWithOptions(ctx, p, "cancel", cwd, false, JournalOptions{}, SteeringOptions{Source: &armedSource{}, OnStart: cancel}, SessionOptions{ResumeID: id}, ExecutionOptions{SessionKey: "muse-test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupt: %v", err)
	}
	raw, err := os.ReadFile(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"reasoning"`) {
		t.Fatalf("lost reasoning: %s", raw)
	}
	if strings.Count(strings.Join(host.calls, ","), "approval/decide") != 3 {
		t.Fatalf("duplicate approval decision: %v", host.calls)
	}
	var initializes, starts, resumes int
	for _, m := range host.calls {
		switch m {
		case "initialize":
			initializes++
		case "session/start":
			starts++
		case "session/resume":
			resumes++
		}
	}
	if initializes != 2 || starts != 1 || resumes != 1 {
		t.Fatalf("wrong lifecycle: %v", host.calls)
	}
}
