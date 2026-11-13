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

func TestCallToolsBatchAndBargeIn(t *testing.T) {
	page, browser := pair(t)
	provider, fake := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := make(chan string)
	dispatched := make(chan string, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- run(ctx, page, provider, func(_ context.Context, s string) error { dispatched <- s; return nil }, backend, time.Minute)
	}()
	expect(t, fake, "session.update")
	send(t, fake, message{"type": "session.updated"})
	expect(t, browser, "ready")
	expect(t, fake, "response.create")
	send(t, fake, message{"type": "response.function_call_arguments.done", "name": "dispatch", "call_id": "d1", "arguments": "{\"prompt\":\"build it\"}"})
	select {
	case p := <-dispatched:
		if p != "build it" {
			t.Fatal(p)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch not called")
	}
	expect(t, fake, "conversation.item.create")
	send(t, fake, message{"type": "response.function_call_arguments.done", "name": "remain_silent", "call_id": "s1", "arguments": "{}"})
	expect(t, fake, "conversation.item.create")
	backend <- "one"
	backend <- "two"
	send(t, fake, message{"type": "response.done"})
	expect(t, browser, "response_done")
	m := expect(t, fake, "conversation.item.create")
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), "one\\n\\ntwo") {
		t.Fatalf("not batched: %s", b)
	}
	expect(t, fake, "response.create")
	send(t, fake, message{"type": "response.output_audio.delta", "item_id": "audio1", "delta": "AAA="})
	expect(t, browser, "transcript")
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	kind, _, err := browser.Read(readCtx)
	if err != nil || kind != websocket.MessageBinary {
		t.Fatalf("audio: %v %v", kind, err)
	}
	send(t, browser, message{"type": "played", "item_id": "audio1", "audio_end_ms": 123})
	// A backend response is still busy: interrupt and retain its unplayed truncation.
	send(t, fake, message{"type": "input_audio_buffer.speech_started"})
	expect(t, fake, "response.cancel")
	trunc := expect(t, fake, "conversation.item.truncate")
	if trunc["item_id"] != "audio1" {
		t.Fatal(trunc)
	}
	expect(t, browser, "speech_started")
	send(t, fake, message{"type": "input_audio_buffer.committed"})
	send(t, fake, message{"type": "response.done"})
	expect(t, browser, "response_done")
	expect(t, fake, "response.create")
	cancel()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("call leaked")
	}
}

func TestIdleAndSingleCall(t *testing.T) {
	page, browser := pair(t)
	provider, fake := pair(t)
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), page, provider, func(context.Context, string) error { return nil }, nil, 25*time.Millisecond)
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

func TestReadinessAndOwnerSpeechDeferBackend(t *testing.T) {
	page, browser := pair(t)
	provider, fake := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := make(chan string)
	go func() { _ = run(ctx, page, provider, nil, backend, time.Minute) }()
	expect(t, fake, "session.update")
	backend <- "early"
	send(t, fake, message{"type": "session.updated"})
	expect(t, browser, "ready")
	expect(t, fake, "response.create")
	send(t, fake, message{"type": "input_audio_buffer.speech_started"})
	expect(t, fake, "response.cancel")
	expect(t, browser, "speech_started")
	send(t, fake, message{"type": "response.done"})
	expect(t, browser, "response_done")
	backend <- "later"
	send(t, fake, message{"type": "input_audio_buffer.committed"})
	// The owner's turn wins. No backend conversation item or extra response was sent.
	expect(t, fake, "response.create")
	send(t, fake, message{"type": "response.done"})
	expect(t, browser, "response_done")
	expect(t, fake, "conversation.item.create")
	expect(t, fake, "response.create")
}
