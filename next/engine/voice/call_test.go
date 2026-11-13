package voice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func pair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- c
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	t.Cleanup(func() { c.CloseNow(); peer.CloseNow() })
	return c, peer
}
func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := writeJSON(ctx, c, v); err != nil {
		t.Fatal(err)
	}
}
func receive(t *testing.T, c *websocket.Conn) message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func expect(t *testing.T, c *websocket.Conn, kind string) message {
	t.Helper()
	m := receive(t, c)
	if m["type"] != kind {
		t.Fatalf("want %s, got %v", kind, m)
	}
	return m
}

func TestIdleAndSingleCall(t *testing.T) {
	page, browser := pair(t)
	provider, fake := pair(t)
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), page, provider, func(context.Context, string) (string, error) { return "", nil }, nil, 25*time.Millisecond)
	}()
	expect(t, fake, "session.update")
	expect(t, browser, "closed")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var m Manager
	hold := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = m.Start(ctx, "room", page, func(ctx context.Context) (*websocket.Conn, error) { close(hold); <-ctx.Done(); return nil, ctx.Err() }, nil, nil)
	}()
	<-hold
	if m.Status() != "room" {
		t.Fatal(m.Status())
	}
	if err := m.Start(ctx, "other", page, nil, nil, nil); err == nil {
		t.Fatal("second call accepted")
	}
	m.Stop()
}

func TestCodexHandoffAndBargeIn(t *testing.T) {
	page, browser := pair(t)
	provider, fake := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := make(chan string)
	started := make(chan string, 1)
	finish := make(chan string)
	go func() {
		_ = run(ctx, page, provider, func(ctx context.Context, p string) (string, error) {
			started <- p
			select {
			case text := <-finish:
				return text, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}, backend, time.Minute)
	}()
	expect(t, fake, "session.update")
	send(t, fake, message{"type": "session.updated"})
	expect(t, browser, "ready")
	send(t, fake, message{"type": "response.function_call_arguments.done", "name": "background_agent", "call_id": "d1", "arguments": "{\"prompt\":\"build it\"}"})
	select {
	case p := <-started:
		if p != "build it" {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	backend <- "working"
	m := expect(t, fake, "conversation.item.create")
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), "[BACKEND] working") {
		t.Fatal(string(b))
	}
	finish <- "done"
	m = expect(t, fake, "conversation.item.create")
	b, _ = json.Marshal(m)
	if !strings.Contains(string(b), "function_call_output") {
		t.Fatal(string(b))
	}
	expect(t, fake, "response.create")
	send(t, fake, message{"type": "response.function_call_arguments.done", "name": "remain_silent", "call_id": "s1", "arguments": "{}"})
	m = expect(t, fake, "conversation.item.create")
	if m["item"].(map[string]any)["output"] != "" {
		t.Fatal(m)
	}
	send(t, fake, message{"type": "response.audio.delta", "item_id": "a1", "delta": "AAA="})
	expect(t, browser, "transcript")
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	kind, _, err := browser.Read(readCtx)
	if err != nil || kind != websocket.MessageBinary {
		t.Fatalf("audio: %v %v", kind, err)
	}
	send(t, browser, message{"type": "played", "item_id": "a1", "audio_end_ms": 123})
	send(t, fake, message{"type": "input_audio_buffer.speech_started"})
	expect(t, browser, "speech_started")
	// No engine response.cancel: provider VAD owns interruption.
	m = expect(t, fake, "conversation.item.truncate")
	if m["item_id"] != "a1" {
		t.Fatal(m)
	}
	send(t, fake, message{"type": "response.done"})
	expect(t, browser, "response_done")
}

func TestOutputBudget(t *testing.T) {
	var b outputBudget
	got := b.append(strings.Repeat("a", 3000)) + b.append(strings.Repeat("z", 3000)) + b.finish()
	if len(got) > 4000 || !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "zzzz") || !strings.Contains(got, truncationMarker) {
		t.Fatalf("bad budget %d", len(got))
	}
}
