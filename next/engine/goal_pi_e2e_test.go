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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
	"github.com/resident-labs/dyna/room"
)

// journalProvider answers Pi's Anthropic requests. The first calls the goal tool Pi declares; the next ends the turn.
type journalProvider struct {
	mu     sync.Mutex
	bodies []string
}

func (p *journalProvider) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(data, &body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(data))
	first := len(p.bodies) == 1
	p.mu.Unlock()
	tool := ""
	for _, declared := range body.Tools {
		if strings.HasSuffix(declared.Name, "__goal") {
			tool = declared.Name
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`)
	if first && tool != "" {
		input, _ := json.Marshal(`{"action":"journal","goal":"g1","text":"nudged the child"}`)
		send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, tool))
		send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, input))
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`)
		send("message_stop", `{"type":"message_stop"}`)
		return
	}
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looked after it."}}`)
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

// TestCheckinReachesRealPiAndItsGoalToolReachesTheEngine runs the real Pi: the engine's check-in wakes a parent over its box
// channel, the model reads it with dyna_goal in context, calls the goal tool through the real MCP server, and the journal
// line lands in the store and clears the stall. Set DYNA_PI_E2E_CLI to a built Pi cli.js to run it.
func TestCheckinReachesRealPiAndItsGoalToolReachesTheEngine(t *testing.T) {
	cli := os.Getenv("DYNA_PI_E2E_CLI")
	if cli == "" {
		t.Skip("DYNA_PI_E2E_CLI names a built Pi cli.js; this test runs that real Pi")
	}
	r := newGoalRig(t)
	lead := asSession(r.lead)
	own, e := r.s.GoalSet(r.ctx, asSession(r.root), contract.GoalSetRequest{Session: r.lead, Text: "lead the port", DoneWhen: "every child is merged"})
	if e != nil {
		t.Fatal(e)
	}
	r.turn(r.lead, "on it")
	r.turn(r.root, "good")
	r.turn(r.child, "working on it")
	r.turn(r.lead, "good")
	r.advance(room.CheckinIdle + time.Minute)
	if err := r.s.Checkins(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.wantCheckins(r.lead, 1)

	dyna, err := dynamcp.New(func(ctx context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
		if method != contract.MethodGoalJournal {
			return nil, &contract.Error{Code: contract.ErrUnknownAction, Message: string(method)}
		}
		var request contract.GoalJournalRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, invalid(err.Error())
		}
		response, problem := r.s.GoalJournal(ctx, lead, request)
		if problem != nil {
			return nil, problem
		}
		out, _ := json.Marshal(response)
		return out, nil
	}, nil, post.Preparer{})
	if err != nil {
		t.Fatal(err)
	}
	mcp := httptest.NewServer(dyna)
	t.Cleanup(mcp.Close)
	provider := &journalProvider{}
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
	mcpConfig, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"dyna": map[string]any{"url": mcp.URL + "/dyna/", "exposure": "direct", "description": dyna.Description(), "timeout": 600}}})
	if err != nil {
		t.Fatal(err)
	}
	modelsPath, mcpPath := filepath.Join(dir, "models.json"), filepath.Join(dir, "mcp.json")
	for path, data := range map[string][]byte{modelsPath: models, mcpPath: mcpConfig} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(r.ctx, 2*time.Minute)
	defer cancel()
	h, err := pi.Open(ctx, "node", []string{cli, "--provider", "anthropic", "--model", "m", "--thinking", "low", "--mode", "rpc", "--models-file", modelsPath, "--session-dir", sessions, "--mcp-config", mcpPath, "--exclude-tools", "bash", "--continue"}, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })

	// The real path a parent wakes by: the engine's box channel hands the box a turn with its sections and inputs.
	r.exec("UPDATE boxes SET state='starting' WHERE session=?", r.lead)
	if _, problem := r.s.BoxHello(r.ctx, lead, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	channel, problem := r.s.BoxChannel(ctx, lead)
	if problem != nil {
		t.Fatal(problem)
	}
	var start contract.TurnStart
	for start.Turn == "" {
		select {
		case message := <-channel:
			if turn, ok := message.Body.(contract.TurnStart); ok {
				start = turn
			}
		case <-ctx.Done():
			t.Fatal("no turn reached the box")
		}
	}
	if err := h.SetPromptSections(ctx, start.Sections); err != nil {
		t.Fatal(err)
	}
	ended, err := h.Start(ctx, start.Turn, start.Inputs, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if ended.Outcome != contract.TurnCompleted || ended.Final != "Looked after it." {
		t.Fatalf("turn %+v", ended)
	}
	if _, problem := r.s.TurnOutput(r.ctx, lead, contract.TurnOutputRequest{Turn: start.Turn, Outcome: ended.Outcome, Final: ended.Final, Model: "m"}); problem != nil {
		t.Fatal(problem)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.bodies) < 2 {
		t.Fatalf("%d model requests", len(provider.bodies))
	}
	first := provider.bodies[0]
	for _, want := range []string{"Check-in: these sessions hold open goals", "child: idle for", "ship the port", "From the Dyna engine", "No reply is expected", string(own.Goal) + ": lead the port", "every child is merged"} {
		if !strings.Contains(strings.ReplaceAll(first, `\"`, `"`), want) {
			t.Fatalf("the first model request lacks %q:\n%s", want, first)
		}
	}
	if !strings.Contains(provider.bodies[1], `"tool_result"`) {
		t.Fatalf("the tool result did not reach the model:\n%s", provider.bodies[1])
	}
	journal, problem := r.s.GoalJournal(r.ctx, ownerCaller, contract.GoalJournalRequest{Room: r.room, Goal: "g1"})
	if problem != nil || len(journal.Entries) != 1 || journal.Entries[0].Text != "nudged the child" || journal.Entries[0].By.Session != r.lead {
		t.Fatalf("journal %+v: %v", journal, problem)
	}
}
