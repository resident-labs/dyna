package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

// steerProvider holds Pi's first request until the steer has reached Pi, answers it with a tool call, and ends the turn on the next.
type steerProvider struct {
	mu      sync.Mutex
	bodies  []string
	release chan struct{}
}

func (p *steerProvider) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(data))
	first := len(p.bodies) == 1
	p.mu.Unlock()
	if first {
		<-p.release
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
	if first {
		input, _ := json.Marshal(`{"path":"/nonexistent"}`)
		send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`)
		send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, input))
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`)
		send("message_stop", `{"type":"message_stop"}`)
		return
	}
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Answered the steer."}}`)
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

// TestSteerReadByRealPiTakesItsPlace runs the real Pi: the engine hands a running turn the owner's DM as a steer, Pi reads it
// between its steps and writes its own record, and transcript.read returns it there. Set DYNA_PI_E2E_CLI to a built Pi cli.js to run it.
func TestSteerReadByRealPiTakesItsPlace(t *testing.T) {
	cli := os.Getenv("DYNA_PI_E2E_CLI")
	if cli == "" {
		t.Skip("DYNA_PI_E2E_CLI names a built Pi cli.js; this test runs that real Pi")
	}
	s, base, caller := transcriptCaller(t)
	ctx, cancel := context.WithTimeout(base, 2*time.Minute)
	defer cancel()
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	provider := &steerProvider{release: make(chan struct{})}
	model := httptest.NewServer(http.HandlerFunc(provider.serve))
	t.Cleanup(model.Close)
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(dir, "agent"))
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("PI_NO_ENV_CREDENTIALS", "1")
	models, err := json.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{"baseUrl": model.URL, "apiKey": "box", "models": []any{
		map[string]any{"id": "m", "name": "m", "api": "anthropic-messages", "reasoning": true, "input": []string{"text"}, "contextWindow": 1000000, "maxTokens": 8000, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, models, 0600); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	h, err := pi.Open(ctx, "node", []string{cli, "--provider", "anthropic", "--model", "m", "--thinking", "low", "--mode", "rpc", "--models-file", modelsPath, "--session-dir", sessions, "--exclude-tools", "bash", "--continue"}, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })

	channel, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start, ok := (<-channel).Body.(contract.TurnStart)
	if !ok {
		t.Fatal("expected start")
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	text := "levzzz will review it"
	sent, problem := s.Post(ctx, owner, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: caller.Session}, Text: text})
	if problem != nil {
		t.Fatal(problem)
	}
	steer, ok := (<-channel).Body.(contract.TurnSteer)
	if !ok {
		t.Fatal("expected steer")
	}
	accepted := make(chan struct{})
	type result struct {
		ended contract.TurnEnded
		err   error
	}
	done := make(chan result, 1)
	go func() {
		ended, err := h.Start(ctx, start.Turn, start.Inputs, func() { close(accepted) })
		done <- result{ended, err}
	}()
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("Pi did not accept the turn")
	}
	if err := h.Steer(ctx, steer.Input); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	finished := <-done
	if finished.err != nil || finished.ended.Outcome != contract.TurnCompleted || finished.ended.Final != "Answered the steer." {
		t.Fatalf("turn %+v: %v", finished.ended, finished.err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.bodies) != 2 || strings.Contains(provider.bodies[0], text) || !strings.Contains(provider.bodies[1], text) {
		t.Fatalf("%d model requests; the steer must reach only the second", len(provider.bodies))
	}

	data, err := os.ReadFile(h.SessionFile())
	if err != nil {
		t.Fatal(err)
	}
	if _, problem := appendTranscript(s, ctx, caller, "pi.jsonl", 0, string(data)); problem != nil {
		t.Fatal(problem)
	}
	page, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	if problem != nil {
		t.Fatal(problem)
	}
	if len(page.Turns) != 1 {
		t.Fatalf("turns %+v", page.Turns)
	}
	var kinds []contract.TranscriptItemKind
	for _, item := range page.Turns[0].Items {
		kinds = append(kinds, item.Kind)
		if item.Kind == contract.TranscriptSteer && (item.Event != sent.Event || item.Text != text) {
			t.Fatalf("steer %+v", item)
		}
	}
	if want := []contract.TranscriptItemKind{contract.TranscriptTool, contract.TranscriptSteer, contract.TranscriptText}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
}
