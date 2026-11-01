package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
)

func TestTransferModelRPC(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	h := &Harness{stdin: inputWriter, pending: make(map[string]chan event), events: make(chan event, 1), done: make(chan struct{}), stop: make(chan struct{})}
	go h.read(outputReader)
	defer inputWriter.Close()
	defer outputWriter.Close()
	var labels []string
	h.Activity = func(_ contract.TurnID, label string) { labels = append(labels, label) }
	go func() {
		reader := bufio.NewReader(inputReader)
		for _, reply := range []string{
			`{"id":"%s","type":"response","command":"transfer_model","success":true,"data":{"path":"boundary","reason":"quota exhausted","tokensBefore":180000,"tokensAfter":4000}}`,
			`{"id":"%s","type":"response","command":"transfer_model","success":false,"error":"Model not found: openai/gone"}`,
		} {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var sent struct{ ID string }
			_ = json.Unmarshal(line, &sent)
			if !strings.Contains(string(line), `"type":"transfer_model","provider":"openai","modelId":"m","thinkingLevel":"high","compactionTriggerTokens":150000`) {
				fmt.Fprintf(outputWriter, `{"id":"%s","type":"response","success":false,"error":"bad command: %s"}`+"\n", sent.ID, strings.ReplaceAll(string(line), `"`, `'`))
				continue
			}
			fmt.Fprintf(outputWriter, reply+"\n", sent.ID)
		}
	}()
	model := contract.ModelChoice{Provider: contract.ModelProviderOpenAI, ID: "m", Thinking: contract.ThinkingHigh}
	got, err := h.TransferModel(context.Background(), "turn_a", model, 150000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "boundary" || got.Reason != "quota exhausted" || got.TokensBefore != 180000 || got.TokensAfter != 4000 {
		t.Fatalf("transfer: %+v", got)
	}
	if len(labels) != 1 || labels[0] != "changing model" {
		t.Fatalf("activity: %v", labels)
	}
	if _, err := h.TransferModel(context.Background(), "turn_a", model, 150000); !errors.Is(err, harness.ErrRefused) || !strings.Contains(err.Error(), "Model not found") {
		t.Fatalf("refusal: %v", err)
	}
}

// fakeProvider answers Pi's Anthropic requests. It stands in for the engine's model route, so it can refuse a model
// the way the route does, and it records what each model received.
type fakeProvider struct {
	mu       sync.Mutex
	requests []fakeRequest
	refuse   map[string]bool // models the route forbids
	hang     map[string]bool // models that never answer until the client leaves
	reply    func(request fakeRequest) string
	tool     *fakeTool // answered once, to the next model request after a summary
}

type fakeTool struct{ name, input string }

type fakeRequest struct {
	Model    string
	Body     string
	Summary  bool
	ToolDone bool
}

func (f *fakeProvider) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(data, &body)
	request := fakeRequest{Model: body.Model, Body: string(data), Summary: strings.Contains(string(data), "checkpoint"), ToolDone: strings.Contains(string(data), `"tool_result"`)}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	refused, hung, tool := f.refuse[body.Model], f.hang[body.Model], f.tool
	if tool != nil && !request.Summary {
		f.tool = nil
	}
	f.mu.Unlock()
	if refused {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":"model_forbidden","message":"The profile does not allow this model."}`)
		return
	}
	if hung {
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	input := len(data) / 4
	send("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`, body.Model, input))
	if tool != nil && !request.Summary {
		send("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, tool.name))
		encoded, _ := json.Marshal(tool.input)
		send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":%s}}`, encoded))
		send("content_block_stop", `{"type":"content_block_stop","index":0}`)
		send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`)
		send("message_stop", `{"type":"message_stop"}`)
		return
	}
	text := "ok"
	if f.reply != nil {
		text = f.reply(request)
	}
	encoded, _ := json.Marshal(text)
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, encoded))
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":%d}}`, len(text)/4))
	send("message_stop", `{"type":"message_stop"}`)
}

func (f *fakeProvider) since(start int) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests[start:]...)
}

func (f *fakeProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeProvider) set(change func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change()
}

// piBox runs a real Pi process with the arguments a box gives it.
type piBox struct {
	t        *testing.T
	cli      string
	dir      string
	models   string
	sessions string
}

func newPiBox(t *testing.T, provider *fakeProvider) *piBox {
	t.Helper()
	cli := os.Getenv("DYNA_PI_E2E_CLI")
	if cli == "" {
		t.Skip("DYNA_PI_E2E_CLI names a built Pi cli.js; this test runs that real Pi")
	}
	server := httptest.NewServer(http.HandlerFunc(provider.serve))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	box := &piBox{t: t, cli: cli, dir: dir, models: filepath.Join(dir, "models.json"), sessions: filepath.Join(dir, "sessions")}
	if err := os.MkdirAll(box.sessions, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(dir, "agent"))
	t.Setenv("PI_OFFLINE", "1")
	t.Setenv("PI_NO_ENV_CREDENTIALS", "1")
	model := func(id string, window int) map[string]any {
		return map[string]any{"id": id, "name": id, "api": "anthropic-messages", "reasoning": true, "input": []string{"text"}, "contextWindow": window, "maxTokens": 8000, "cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}}
	}
	config, err := json.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{
		"baseUrl": server.URL, "apiKey": "box",
		"models": []any{model("old-large", 1000000), model("new-small", 40000), model("new-huge", 2000000)},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(box.models, config, 0600); err != nil {
		t.Fatal(err)
	}
	return box
}

// start opens Pi on model, as box.hello does after a stop.
func (b *piBox) start(model string, compactTokens int) *Harness {
	b.t.Helper()
	args := []string{b.cli, "--provider", "anthropic", "--model", model, "--thinking", "low"}
	if compactTokens > 0 {
		args = append(args, "--compaction-trigger-tokens", fmt.Sprint(compactTokens))
	}
	args = append(args, "--mode", "rpc", "--models-file", b.models, "--session-dir", b.sessions, "--exclude-tools", "bash", "--continue")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	b.t.Cleanup(cancel)
	h, err := Open(ctx, "node", args, "", b.dir)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { _ = h.Close() })
	return h
}

func (b *piBox) turn(h *Harness, delivery int, text string) contract.TurnEnded {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	input := contract.TurnInput{Delivery: contract.DeliverySeq(delivery), Text: text}
	result, err := h.Start(ctx, contract.TurnID(fmt.Sprint("turn_", delivery)), []contract.TurnInput{input}, func() {})
	if err != nil {
		b.t.Fatal(err)
	}
	return result
}

func (b *piBox) transfer(h *Harness, model string, compactTokens int) (path, reason string) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	moved, err := h.TransferModel(ctx, "turn_t", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: model, Thinking: contract.ThinkingMedium}, compactTokens)
	if err != nil {
		b.t.Fatal(err)
	}
	return moved.Path, moved.Reason
}

// fill runs turns on the old model until the session holds about 70,000 tokens, too much for new-small.
func (b *piBox) fill(h *Harness, provider *fakeProvider) {
	b.t.Helper()
	provider.set(func() { provider.reply = func(fakeRequest) string { return strings.Repeat("filler answer ", 5000) } })
	for i := 1; i <= 4; i++ {
		if got := b.turn(h, i, fmt.Sprintf("question %d UNIQUE-MARKER-%d", i, i)); got.Outcome != contract.TurnCompleted {
			b.t.Fatalf("turn %d: %+v", i, got)
		}
	}
	provider.set(func() {
		provider.reply = func(request fakeRequest) string {
			if request.Summary {
				return "## Goal\nthe real summary from the old model"
			}
			return "done"
		}
	})
}

func entries(t *testing.T, file string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		result = append(result, entry)
	}
	return result
}

func lastModelChange(t *testing.T, file string) string {
	t.Helper()
	found := ""
	for _, entry := range entries(t, file) {
		if entry["type"] == "model_change" {
			found, _ = entry["modelId"].(string)
		}
	}
	return found
}

func models(requests []fakeRequest) (summaries, turns []string) {
	for _, request := range requests {
		if request.Summary {
			summaries = append(summaries, request.Model)
		} else {
			turns = append(turns, request.Model)
		}
	}
	return summaries, turns
}

func TestTransferEndToEnd(t *testing.T) {
	t.Run("fits and switches", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		box.turn(h, 1, "hello")
		start := provider.count()
		if path, _ := box.transfer(h, "new-small", 0); path != "switched" {
			t.Fatalf("path %q", path)
		}
		box.turn(h, 2, "next")
		summaries, turns := models(provider.since(start))
		if len(summaries) != 0 || len(turns) != 1 || turns[0] != "new-small" {
			t.Fatalf("summaries %v turns %v", summaries, turns)
		}
	})

	t.Run("compacts on the old model", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		box.fill(h, provider)
		start := provider.count()
		if path, reason := box.transfer(h, "new-small", 0); path != "compacted" {
			t.Fatalf("path %q reason %q", path, reason)
		}
		box.turn(h, 5, "next")
		requests := provider.since(start)
		summaries, turns := models(requests)
		if len(summaries) == 0 || summaries[0] != "old-large" || len(turns) != 1 || turns[0] != "new-small" {
			t.Fatalf("summaries %v turns %v", summaries, turns)
		}
		last := requests[len(requests)-1]
		if !strings.Contains(last.Body, "the real summary from the old model") || len(last.Body)/4 > 40000 {
			t.Fatalf("new model got %d tokens", len(last.Body)/4)
		}
		if lastModelChange(t, h.SessionFile()) != "new-small" {
			t.Fatal("the session file does not record the new model")
		}
	})

	t.Run("compacts over 100k even when the new model holds it", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		provider.set(func() { provider.reply = func(fakeRequest) string { return strings.Repeat("filler answer ", 40000) } })
		for i := 1; i <= 4; i++ {
			box.turn(h, i, "question")
		}
		provider.set(func() { provider.reply = func(fakeRequest) string { return "## Goal\nsummary" } })
		start := provider.count()
		if path, reason := box.transfer(h, "new-huge", 0); path != "compacted" {
			t.Fatalf("path %q reason %q", path, reason)
		}
		if summaries, _ := models(provider.since(start)); len(summaries) == 0 || summaries[0] != "old-large" {
			t.Fatalf("summaries %v", summaries)
		}
	})

	// The owner's fallback. The route forbids the old model, as it does when a profile no longer allows it, and the new
	// model then reads the earlier conversation back with Pi's own read tool.
	t.Run("boundary after the old model is refused", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		box.fill(h, provider)
		provider.set(func() { provider.refuse = map[string]bool{"old-large": true} })
		start := provider.count()
		path, reason := box.transfer(h, "new-small", 0)
		if path != "boundary" || !strings.Contains(reason, "403") {
			t.Fatalf("path %q reason %q", path, reason)
		}
		file := h.SessionFile()
		input, _ := json.Marshal(map[string]string{"path": file})
		provider.set(func() { provider.tool = &fakeTool{name: "read", input: string(input)} })
		box.turn(h, 5, "next")
		requests := provider.since(start)
		first := requests[len(requests)-2]
		if first.Model != "new-small" || !strings.Contains(first.Body, file) || !strings.Contains(first.Body, "Read it for the context") {
			t.Fatalf("the new model got no boundary: model %s", first.Model)
		}
		if strings.Contains(first.Body, "UNIQUE-MARKER-1") {
			t.Fatal("the old conversation reached the new model")
		}
		second := requests[len(requests)-1]
		if !second.ToolDone || !strings.Contains(second.Body, "UNIQUE-MARKER-1") {
			t.Fatalf("the read tool did not return the earlier conversation: %.300s", second.Body)
		}
		for _, entry := range entries(t, file) {
			if entry["type"] == "message" {
				return
			}
		}
		t.Fatal("the session file lost its messages")
	})

	// box.hello starts Pi on the new model, so no change is visible in-process.
	t.Run("stopped box", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		box.fill(h, provider)
		file := h.SessionFile()
		if err := h.Close(); err != nil {
			t.Fatal(err)
		}
		restarted := box.start("new-small", 0)
		if restarted.SessionFile() != file {
			t.Fatalf("continued %s, want %s", restarted.SessionFile(), file)
		}
		start := provider.count()
		if path, reason := box.transfer(restarted, "new-small", 0); path != "compacted" {
			t.Fatalf("path %q reason %q", path, reason)
		}
		box.turn(restarted, 5, "next")
		summaries, turns := models(provider.since(start))
		if len(summaries) == 0 || summaries[0] != "old-large" || len(turns) != 1 || turns[0] != "new-small" {
			t.Fatalf("summaries %v turns %v", summaries, turns)
		}
		if lastModelChange(t, file) != "new-small" {
			t.Fatal("the session file does not record the new model")
		}
	})

	t.Run("restart on an unchanged model does nothing", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		box.fill(h, provider)
		if err := h.Close(); err != nil {
			t.Fatal(err)
		}
		restarted := box.start("old-large", 0)
		start := provider.count()
		if path, _ := box.transfer(restarted, "old-large", 0); path != "unchanged" {
			t.Fatalf("path %q", path)
		}
		if got := provider.since(start); len(got) != 0 {
			t.Fatalf("requests: %d", len(got))
		}
	})

	// --model takes an ID the catalog does not know, so the first turn's transfer must too.
	t.Run("a custom model ID", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("custom-model", 0)
		if path, reason := box.transfer(h, "custom-model", 0); path != "unchanged" {
			t.Fatalf("path %q reason %q", path, reason)
		}
		box.turn(h, 1, "hello")
		if path, reason := box.transfer(h, "other-custom-model", 0); path != "switched" {
			t.Fatalf("path %q reason %q", path, reason)
		}
		box.turn(h, 2, "next")
		requests := provider.since(0)
		if last := requests[len(requests)-1]; last.Model != "other-custom-model" {
			t.Fatalf("model %q", last.Model)
		}
	})

	t.Run("trigger follows the transfer", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 500000)
		box.fill(h, provider)
		start := provider.count()
		if path, reason := box.transfer(h, "old-large", 30000); path != "compacted" {
			t.Fatalf("path %q reason %q", path, reason)
		}
		if summaries, _ := models(provider.since(start)); len(summaries) == 0 {
			t.Fatal("the new trigger did not compact")
		}
	})

	t.Run("stop cancels the compaction and keeps the model", func(t *testing.T) {
		provider := &fakeProvider{}
		box := newPiBox(t, provider)
		h := box.start("old-large", 0)
		box.fill(h, provider)
		provider.set(func() { provider.hang = map[string]bool{"old-large": true} })
		start := provider.count()
		type outcome struct {
			path string
			err  error
		}
		done := make(chan outcome, 1)
		go func() {
			moved, err := h.TransferModel(context.Background(), "turn_t", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "new-small", Thinking: contract.ThinkingMedium}, 0)
			done <- outcome{moved.Path, err}
		}()
		deadline := time.Now().Add(30 * time.Second)
		for provider.count() == start {
			if time.Now().After(deadline) {
				t.Fatal("no summary request")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := h.Abort(context.Background()); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-done:
			if got.err != nil || got.path != "aborted" {
				t.Fatalf("%+v", got)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the transfer did not stop")
		}
		if lastModelChange(t, h.SessionFile()) != "old-large" {
			t.Fatal("the model changed")
		}
	})
}
