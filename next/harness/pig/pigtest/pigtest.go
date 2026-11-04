// Package pigtest runs PiG sessions in tests against a scripted Anthropic endpoint, as a box runs them against its
// loopback model route.
package pigtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Block is one content block of a scripted response: text, or a tool call when Tool is set.
type Block struct {
	Text  string
	Tool  string
	Input string // the tool call's arguments as JSON
}

// Model is an Anthropic Messages endpoint that answers each request with the next scripted response, and keeps every
// request. With the script used up it answers "script ended".
type Model struct {
	// InputTokens is the input each response reports using; zero reports 10.
	InputTokens int
	mu          sync.Mutex
	script      [][]Block
	requests    []Request
}

// Request is the part of a request the tests read.
type Request struct {
	System   json.RawMessage   `json:"system"`
	Tools    []json.RawMessage `json:"tools"`
	Messages []json.RawMessage `json:"messages"`
}

// NewModel scripts the responses in order.
func NewModel(responses ...[]Block) *Model { return &Model{script: responses} }

func (m *Model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var request Request
	_ = json.Unmarshal(body, &request)
	m.mu.Lock()
	m.requests = append(m.requests, request)
	blocks := []Block{{Text: "script ended"}}
	if len(m.script) > 0 {
		blocks, m.script = m.script[0], m.script[1:]
	}
	n, input := len(m.requests), m.InputTokens
	m.mu.Unlock()
	if input == 0 {
		input = 10
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(event string, data any) {
		encoded, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encoded)
	}
	send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant", "model": "test-model", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": input, "output_tokens": 1}}})
	stop := "end_turn"
	for i, b := range blocks {
		if b.Tool == "" {
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}})
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": b.Text}})
		} else {
			stop = "tool_use"
			send("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d_%d", n, i), "name": b.Tool, "input": map[string]any{}}})
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": b.Input}})
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 5}})
	send("message_stop", map[string]any{"type": "message_stop"})
}

// Requests returns the requests the model received so far.
func (m *Model) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.requests...)
}

// AgentDir serves the model and writes an agent folder whose anthropic provider is it, with the models test-model and
// small-model, whose context window is 30,000 tokens, as a
// box's loopback models.json does. mcp.json lists the given servers.
func AgentDir(t *testing.T, model *Model, mcpServers string) string {
	t.Helper()
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	if mcpServers == "" {
		mcpServers = "{}"
	}
	for name, content := range map[string]string{
		"models.json":   `{"providers":{"anthropic":{"baseUrl":"` + server.URL + `","apiKey":"box","models":[{"id":"test-model","api":"anthropic-messages","reasoning":true,"input":["text","image"],"contextWindow":200000,"maxTokens":8000},{"id":"small-model","api":"anthropic-messages","reasoning":true,"input":["text","image"],"contextWindow":30000,"maxTokens":4000}]}}}`,
		"settings.json": `{"defaultProjectTrust":"always","steeringMode":"all"}`,
		"auth.json":     `{}`,
		"mcp.json":      `{"mcpServers":` + mcpServers + `}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
