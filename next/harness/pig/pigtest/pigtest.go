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
	"strconv"
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
	Header       http.Header       `json:"-"`
	System       json.RawMessage   `json:"system"`
	Tools        []json.RawMessage `json:"tools"`
	Messages     []json.RawMessage `json:"messages"`
	OutputConfig json.RawMessage   `json:"output_config"`
}

// NewModel scripts the responses in order.
func NewModel(responses ...[]Block) *Model { return &Model{script: responses} }

func (m *Model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var request Request
	_ = json.Unmarshal(body, &request)
	request.Header = r.Header
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
// small-model, whose context window is 30,000 tokens, and tools-model, which takes tool changes mid-conversation as a Claude route does, as a
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
		"models.json":   `{"providers":{"anthropic":{"baseUrl":"` + server.URL + `","apiKey":"box","models":[{"id":"test-model","api":"anthropic-messages","reasoning":true,"input":["text","image"],"contextWindow":200000,"maxTokens":8000},{"id":"small-model","api":"anthropic-messages","reasoning":true,"input":["text","image"],"contextWindow":30000,"maxTokens":4000},{"id":"tools-model","api":"anthropic-messages","reasoning":true,"input":["text"],"contextWindow":200000,"maxTokens":8000,"compat":{"supportsMidConvoSystemMessages":true,"supportsMidConvoToolChanges":true}},{"id":"effort-model","api":"anthropic-messages","reasoning":true,"input":["text"],"contextWindow":200000,"maxTokens":8000,"compat":{"forceAdaptiveThinking":true,"supportsMidConvoEffort":true},"thinkingLevelMap":{"off":null,"low":"low","medium":"medium","high":"high"}}]}}}`,
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

// Package writes a Pi package in a new folder and returns the folder: a skill named fixture-skill, a prompt template
// named fixture-prompt, and one extension file holding extension, which a package's `pi` block lists as Pi's does. An empty extension leaves the
// folder without a file, so the package starts no node.
func Package(t *testing.T, extension string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "extensions"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"package.json":                  `{"name":"fixture-package","version":"1.0.0","pi":{"extensions":["extensions"],"skills":["skills"],"prompts":["prompts"]}}`,
		"extensions/index.js":           extension,
		"skills/fixture-skill/SKILL.md": "---\nname: fixture-skill\ndescription: A skill from the fixture package.\n---\nUse the fixture.\n",
		"prompts/fixture-prompt.md":     "---\ndescription: A prompt from the fixture package.\n---\nDo the fixture thing.\n",
	} {
		if content == "" {
			continue
		}
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ToolExtension is an extension that registers one tool, whose result is the given text.
func ToolExtension(name, result string) string {
	return `export default function (pi) {
  pi.registerTool({ name: ` + strconv.Quote(name) + `, label: "fixture", description: "A tool of the fixture package.", parameters: { type: "object", properties: {} },
    async execute() { return { content: [{ type: "text", text: ` + strconv.Quote(result) + ` }] }; } });
}
`
}

// Packages lists the package folders in the agent folder's settings, as a box start writes them.
func Packages(t *testing.T, agentDir string, folders ...string) {
	t.Helper()
	settings, err := json.Marshal(map[string]any{"defaultProjectTrust": "always", "steeringMode": "all", "packages": folders})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
}
