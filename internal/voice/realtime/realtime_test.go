package realtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// fakeServer is a Realtime WebSocket server. Each client message arrives on
// got; each event sent on reply goes to the client.
type fakeServer struct {
	url    string
	header chan http.Header
	got    chan map[string]any
	reply  chan string

	mu      sync.Mutex
	drop    chan struct{}  // closing it drops the connections open now
	writers sync.WaitGroup // the reply writers of those connections
}

// closeAll drops the server's connections, as a network failure would. It
// returns once no dropped connection can take a reply, so the next reply
// goes to a later connection.
func (f *fakeServer) closeAll() {
	f.mu.Lock()
	close(f.drop)
	f.drop = make(chan struct{})
	f.mu.Unlock()
	f.writers.Wait()
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{header: make(chan http.Header, 4), got: make(chan map[string]any, 16), reply: make(chan string, 16), drop: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		drop := f.drop
		f.writers.Add(1)
		f.mu.Unlock()
		f.header <- r.Header.Clone()
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			f.writers.Done()
			return
		}
		defer ws.Close()
		go func() { <-drop; ws.Close() }()
		go func() {
			defer f.writers.Done()
			for {
				select {
				case <-drop:
					return
				case text := <-f.reply:
					if ws.WriteMessage(websocket.TextMessage, []byte(text)) != nil {
						return
					}
				}
			}
		}()
		for {
			var m map[string]any
			if ws.ReadJSON(&m) != nil {
				return
			}
			f.got <- m
		}
	}))
	t.Cleanup(srv.Close)
	f.url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/realtime?model=test"
	return f
}

func testProvider(f *fakeServer) Provider {
	return OpenAI(f.url, func(context.Context) (string, string, error) { return "tok", "acct", nil })
}

var testSession = Session{
	Instructions:        "Be Voice.",
	Tools:               []Tool{{Name: "agents", Description: "Who works.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
	Voice:               "marin",
	TranscriptionPrompt: "Expect Dyna.",
	Language:            "en",
}

// dial connects to f and completes the session handshake.
func dial(t *testing.T, f *fakeServer) (*Conn, map[string]any) {
	t.Helper()
	f.reply <- `{"type":"session.created"}`
	f.reply <- `{"type":"session.updated"}`
	c, err := Dial(context.Background(), testProvider(f), testSession)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, <-f.got
}

// field reads a nested field of a decoded JSON object by its path.
func field(m any, path ...string) any {
	for _, key := range path {
		obj, _ := m.(map[string]any)
		m = obj[key]
	}
	return m
}

func TestDialSignsInAndSendsTheSession(t *testing.T) {
	f := newFakeServer(t)
	_, update := dial(t, f)
	h := <-f.header
	for name, want := range map[string]string{"Authorization": "Bearer tok", "ChatGPT-Account-ID": "acct", "originator": "codex_cli_rs"} {
		if got := h.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if update["type"] != "session.update" {
		t.Fatalf("first message = %v, want session.update", update)
	}
	s := update["session"]
	for _, c := range []struct {
		path []string
		want any
	}{
		{[]string{"type"}, "realtime"},
		{[]string{"instructions"}, "Be Voice."},
		{[]string{"audio", "input", "transcription", "model"}, OpenAITranscriber},
		{[]string{"audio", "input", "transcription", "prompt"}, "Expect Dyna."},
		{[]string{"audio", "input", "transcription", "language"}, "en"},
		{[]string{"audio", "input", "noise_reduction", "type"}, "near_field"},
		{[]string{"audio", "input", "turn_detection", "type"}, "semantic_vad"},
		{[]string{"audio", "input", "turn_detection", "create_response"}, false},
		{[]string{"audio", "input", "turn_detection", "interrupt_response"}, false},
		{[]string{"audio", "input", "format", "rate"}, float64(24000)},
		{[]string{"audio", "output", "voice"}, "marin"},
	} {
		if got := field(s, c.path...); got != c.want {
			t.Errorf("session.%s = %v, want %v", strings.Join(c.path, "."), got, c.want)
		}
	}
	tools, _ := field(s, "tools").([]any)
	if len(tools) != 1 || field(tools[0], "name") != "agents" || field(tools[0], "type") != "function" {
		t.Errorf("session.tools = %v, want the one flat agents function", tools)
	}
	turn, _ := field(s, "audio", "input", "turn_detection").(map[string]any)
	if _, ok := turn["eagerness"]; ok {
		t.Errorf("default session must not set eagerness")
	}
	eager := testSession
	eager.VADEagerness = "high"
	eagerTurn := openAISession(eager).(msg)["audio"].(msg)["input"].(msg)["turn_detection"].(msg)
	if eagerTurn["eagerness"] != "high" || eagerTurn["type"] != "semantic_vad" || eagerTurn["create_response"] != false {
		t.Errorf("eager turn detection = %v", eagerTurn)
	}
}

func TestDialReportsARefusedSession(t *testing.T) {
	f := newFakeServer(t)
	f.reply <- `{"type":"error","error":{"code":"invalid_value","message":"bad voice"}}`
	_, err := Dial(context.Background(), testProvider(f), testSession)
	if err == nil || !strings.Contains(err.Error(), "bad voice") {
		t.Fatalf("Dial error = %v, want the provider's message", err)
	}
}

func TestDialFailsWithoutCredentials(t *testing.T) {
	p := OpenAI("ws://127.0.0.1:1/v1/realtime", func(context.Context) (string, string, error) {
		return "", "", context.DeadlineExceeded
	})
	if _, err := Dial(context.Background(), p, testSession); err != context.DeadlineExceeded {
		t.Fatalf("Dial error = %v, want the credentials error", err)
	}
}

// runTool runs one tool call through RunTool and decodes its output.
func runTool(t *testing.T, ctx context.Context, tools []Tool, name, args string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(RunTool(ctx, tools, Event{Kind: ToolCall, CallID: "call_1", Name: name, Args: json.RawMessage(args)})), &out); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	return out
}

func TestRunToolPassesItsArguments(t *testing.T) {
	tools := []Tool{{Name: "tell_root", Run: func(_ context.Context, args json.RawMessage) (string, error) {
		var a struct{ Text string }
		_ = json.Unmarshal(args, &a)
		return "Sent: " + a.Text, nil
	}}}
	out := runTool(t, context.Background(), tools, "tell_root", `{"text":"fix CI"}`)
	if out["result"] != "Sent: fix CI" {
		t.Fatalf("output = %v, want the tool's result", out)
	}
}

func TestRunToolReportsAnUnknownTool(t *testing.T) {
	out := runTool(t, context.Background(), nil, "launch", `{}`)
	if !strings.Contains(out["error"].(string), "launch") {
		t.Fatalf("output = %v, want an error naming the tool", out)
	}
}

func TestRunToolStopsWaitingForASlowTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	defer close(release)
	tools := []Tool{{Name: "agents", Run: func(context.Context, json.RawMessage) (string, error) {
		cancel() // the call's time runs out while the tool still works
		<-release
		return "late", nil
	}}}
	out := runTool(t, ctx, tools, "agents", `{}`)
	if !strings.Contains(out["error"].(string), "in time") {
		t.Fatalf("output = %v, want a timeout error", out)
	}
}

func TestToolOutputIsBounded(t *testing.T) {
	out := toolOutput(strings.Repeat("é", MaxToolOutput), nil)
	if len(out) > MaxToolOutput {
		t.Fatalf("output has %d bytes, want at most %d", len(out), MaxToolOutput)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil || !strings.HasSuffix(m["result"], "é…") {
		t.Fatalf("output %q is not a cut result: %v", out[len(out)-20:], err)
	}
}

func TestGADecodesServerEvents(t *testing.T) {
	for _, c := range []struct {
		data string
		want Event
	}{
		{`{"type":"session.updated"}`, Event{Kind: SessionReady}},
		{`{"type":"input_audio_buffer.speech_started","item_id":"i1"}`, Event{Kind: SpeechStarted, ItemID: "i1"}},
		{`{"type":"input_audio_buffer.committed","item_id":"i1"}`, Event{Kind: Committed, ItemID: "i1"}},
		{`{"type":"response.created","response":{"id":"r1","metadata":{"dyna_speak":"m1"}}}`, Event{Kind: ResponseStarted, ResponseID: "r1", Key: "m1"}},
		{`{"type":"response.output_audio.delta","response_id":"r1","item_id":"i2","delta":"AQI="}`, Event{Kind: AudioDelta, ResponseID: "r1", ItemID: "i2", Audio: []byte{1, 2}}},
		{`{"type":"response.output_audio.done","response_id":"r1","item_id":"i2"}`, Event{Kind: AudioDone, ResponseID: "r1", ItemID: "i2"}},
		{`{"type":"response.done","response":{"id":"r1","status":"cancelled"}}`, Event{Kind: ResponseDone, ResponseID: "r1", Text: "cancelled"}},
		{`{"type":"response.function_call_arguments.done","call_id":"c1","name":"agents","arguments":"{}"}`, Event{Kind: ToolCall, CallID: "c1", Name: "agents", Args: json.RawMessage(`{}`)}},
		{`{"type":"conversation.item.input_audio_transcription.delta","delta":"fix"}`, Event{Kind: OwnerPartial, Text: "fix"}},
		{`{"type":"conversation.item.input_audio_transcription.completed","transcript":"fix CI"}`, Event{Kind: OwnerTranscript, Text: "fix CI"}},
		{`{"type":"response.output_audio_transcript.done","transcript":"On it."}`, Event{Kind: VoiceTranscript, Text: "On it."}},
		{`{"type":"error","error":{"code":"x","message":"boom"}}`, Event{Kind: Error, Code: "x", Text: "boom"}},
		{`{"type":"rate_limits.updated"}`, Event{Kind: Other}},
	} {
		got, err := GA{}.Decode([]byte(c.data))
		if err != nil {
			t.Fatalf("%s: %v", c.data, err)
		}
		c.want.Type = field(decode(c.data), "type").(string)
		if gb, wb := mustMarshal(got), mustMarshal(c.want); gb != wb {
			t.Errorf("%s:\n got %s\nwant %s", c.data, gb, wb)
		}
	}
}

func decode(s string) map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func mustMarshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRegistryFindsProvidersByAlias(t *testing.T) {
	r := NewRegistry(OpenAI("ws://x", nil))
	for _, name := range []string{"openai", "GPT-Live", " gpt-realtime "} {
		if p, err := r.Lookup(name); err != nil || p.Key != "openai" {
			t.Errorf("Lookup(%q) = %v, %v", name, p.Key, err)
		}
	}
	if _, err := r.Lookup("minicpm-o"); err == nil || !strings.Contains(err.Error(), "openai") {
		t.Errorf("Lookup of an unknown provider = %v, want an error listing openai", err)
	}
}
