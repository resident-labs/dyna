package jsonrpc

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
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// pipePeer is a minimal in-process JSON-RPC peer over real pipes. Everything
// it reads lands in received; everything it writes goes to the client.
type pipePeer struct {
	mu       sync.Mutex
	out      io.Writer
	reader   *bufio.Reader
	received chan map[string]any
}

func newPipePeer(t *testing.T) (*pipePeer, *os.File, *os.File) {
	t.Helper()
	peerIn, clientOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientIn, peerOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &pipePeer{out: peerOut, reader: bufio.NewReader(peerIn), received: make(chan map[string]any, 16)}
	go p.loop()
	t.Cleanup(func() {
		peerIn.Close()
		peerOut.Close()
		clientIn.Close()
		clientOut.Close()
	})
	return p, clientOut, clientIn
}

func (p *pipePeer) loop() {
	for {
		line, err := p.reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg map[string]any
			if json.Unmarshal(line, &msg) == nil {
				p.received <- msg
			}
		}
		if err != nil {
			close(p.received)
			return
		}
	}
}

func (p *pipePeer) write(msg map[string]any) {
	raw, _ := json.Marshal(msg)
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = p.out.Write(append(raw, '\n'))
}

func (p *pipePeer) reply(id any, result any) {
	raw, _ := json.Marshal(result)
	p.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(raw)})
}

func (p *pipePeer) replyError(id any, code int, message string) {
	p.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

// next returns the next message the peer received.
func (p *pipePeer) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case msg, ok := <-p.received:
		if !ok {
			t.Fatal("peer input closed")
		}
		return msg
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for a message")
	}
	return nil
}

func dialStdioTest(t *testing.T, handlers Handlers) (*pipePeer, *Conn) {
	t.Helper()
	peer, clientOut, clientIn := newPipePeer(t)
	// Closing both directions ends the reader; closing only stdin would
	// leave it waiting on the peer's still-open output pipe.
	kill := func() {
		clientOut.Close()
		clientIn.Close()
	}
	conn := AttachStdio(clientOut, clientIn, nil, kill, nil, handlers)
	t.Cleanup(conn.Close)
	return peer, conn
}

func TestRequestDecodesResult(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	go func() {
		msg := peer.next(t)
		peer.reply(msg["id"], map[string]any{"answer": 42})
	}()
	var out struct{ Answer int }
	if err := conn.Request(context.Background(), "ask", map[string]any{"q": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Answer != 42 {
		t.Fatalf("out=%+v", out)
	}
}

func TestRequestReportsProtocolErrors(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	go func() {
		msg := peer.next(t)
		peer.replyError(msg["id"], -32000, "Authentication required")
	}()
	err := conn.Request(context.Background(), "ask", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.ErrorCode() != -32000 {
		t.Fatalf("error=%v", err)
	}
}

// TestEchoedStringIDReachesItsRequest proves the reply path accepts the exact
// id the client put on the wire. A bare numeric id echoed back as a number
// used to fail decoding and dropped every reply.
func TestEchoedStringIDReachesItsRequest(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	go func() {
		msg := peer.next(t)
		if _, ok := msg["id"].(string); !ok {
			t.Errorf("request id=%#v, want a string", msg["id"])
		}
		peer.reply(msg["id"], map[string]any{})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conn.Request(ctx, "ask", nil, nil); err != nil {
		t.Fatalf("reply with the echoed id was dropped: %v", err)
	}
}

func TestConcurrentRequestsRouteToTheirCallers(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	const calls = 8
	go func() {
		// Hold every request, then answer in reverse order.
		held := make([]map[string]any, 0, calls)
		for len(held) < calls {
			held = append(held, peer.next(t))
		}
		for i := len(held) - 1; i >= 0; i-- {
			params, _ := held[i]["params"].(map[string]any)
			peer.reply(held[i]["id"], map[string]any{"n": params["n"]})
		}
	}()
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for n := 0; n < calls; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var out struct{ N int }
			if err := conn.Request(ctx, "echo", map[string]any{"n": n}, &out); err != nil {
				errs <- err
			} else if out.N != n {
				errs <- fmt.Errorf("request %d got reply for %d", n, out.N)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestNotificationsReachTheHandler(t *testing.T) {
	got := make(chan Notification, 2)
	peer, conn := dialStdioTest(t, Handlers{Notification: func(n Notification) { got <- n }})
	peer.write(map[string]any{"jsonrpc": "2.0", "method": "thing/happened", "params": map[string]any{"k": "v"}})
	select {
	case n := <-got:
		if n.Method != "thing/happened" || string(n.Params) != `{"k":"v"}` || len(n.Raw) == 0 {
			t.Fatalf("notification=%+v", n)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("notification not delivered")
	}

	replaced := make(chan Notification, 1)
	conn.SetNotification(func(n Notification) { replaced <- n })
	peer.write(map[string]any{"jsonrpc": "2.0", "method": "second"})
	select {
	case n := <-replaced:
		if n.Method != "second" {
			t.Fatalf("notification=%+v", n)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("notification not delivered to the replacement handler")
	}
}

func TestNotifySendsNoID(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	if err := conn.Notify("ping", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	msg := peer.next(t)
	if _, hasID := msg["id"]; hasID || msg["method"] != "ping" {
		t.Fatalf("msg=%v", msg)
	}
}

func TestRequestHandlerAnswersPeerRequests(t *testing.T) {
	peer, _ := dialStdioTest(t, Handlers{Request: func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "good":
			return map[string]any{"ok": true}, nil
		case "coded":
			return nil, &Error{Code: CodeInvalidParams, Message: "bad params"}
		}
		return nil, errors.New("boom")
	}})
	for _, tc := range []struct {
		method string
		code   float64
		result string
	}{
		{"good", 0, `{"ok":true}`},
		{"coded", CodeInvalidParams, ""},
		{"plain", CodeInternal, ""},
	} {
		peer.write(map[string]any{"jsonrpc": "2.0", "id": tc.method, "method": tc.method})
		msg := peer.next(t)
		if msg["id"] != tc.method {
			t.Fatalf("%s: answer id=%v", tc.method, msg["id"])
		}
		if tc.code == 0 {
			encoded, _ := json.Marshal(msg["result"])
			if string(encoded) != tc.result {
				t.Fatalf("%s: result=%s", tc.method, encoded)
			}
			continue
		}
		errObj, _ := msg["error"].(map[string]any)
		if errObj["code"] != tc.code {
			t.Fatalf("%s: error=%v", tc.method, msg["error"])
		}
	}
}

func TestPeerRequestWithoutHandlerIsMethodNotFound(t *testing.T) {
	peer, _ := dialStdioTest(t, Handlers{})
	peer.write(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "nope"})
	msg := peer.next(t)
	errObj, _ := msg["error"].(map[string]any)
	if msg["id"] != float64(5) || errObj["code"] != float64(CodeMethodNotFound) {
		t.Fatalf("msg=%v", msg)
	}
}

func TestTransportDeathFailsPending(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- conn.Request(ctx, "never/answered", nil, nil) }()
	peer.next(t) // the request is on the wire; the peer never answers it
	conn.Close()
	select {
	case err := <-done:
		var te *TransportError
		if !errors.As(err, &te) {
			t.Fatalf("error=%v, want a transport error", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("pending call never settled")
	}
}

func TestCloseIsIdempotentAndRunsOnCloseOnce(t *testing.T) {
	_, clientOut, clientIn := newPipePeer(t)
	var mu sync.Mutex
	closes := 0
	conn := AttachStdio(clientOut, clientIn, nil, func() {
		mu.Lock()
		closes++
		mu.Unlock()
		clientOut.Close()
		clientIn.Close()
	}, nil, Handlers{})
	if conn.Done() {
		t.Fatal("new connection reports done")
	}
	conn.Close()
	conn.Close()
	if !conn.Done() {
		t.Fatal("closed connection reports not done")
	}
	mu.Lock()
	defer mu.Unlock()
	if closes != 1 {
		t.Fatalf("onClose ran %d times", closes)
	}
	if err := conn.Notify("late", nil); err == nil {
		t.Fatal("notify on a closed connection succeeded")
	}
	var te *TransportError
	if err := conn.Request(context.Background(), "late", nil, nil); !errors.As(err, &te) {
		t.Fatalf("request on a closed connection: %v", err)
	}
}

// TestDialWebSocketRoundTrip runs one request, one notification and one peer
// request over a real WebSocket endpoint.
func TestDialWebSocketRoundTrip(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != "test-secret" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer socket.Close()
		for {
			kind, raw, err := socket.ReadMessage()
			if err != nil || kind != websocket.TextMessage {
				return
			}
			var msg map[string]any
			if json.Unmarshal(raw, &msg) != nil || msg["method"] != "hello" {
				continue
			}
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": map[string]any{"hi": true}})
			_ = socket.WriteMessage(websocket.TextMessage, reply)
			note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "note", "params": map[string]any{"n": 1}})
			_ = socket.WriteMessage(websocket.TextMessage, note)
			// Keep the connection open until the client goes away, so the
			// notification is not cut off by the server closing first.
			for {
				if _, _, err := socket.ReadMessage(); err != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	notes := make(chan Notification, 2)
	conn, err := DialWebSocket(context.Background(), server.URL+"/ws?server-key=test-secret", Handlers{Notification: func(n Notification) { notes <- n }})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var out struct{ Hi bool }
	if err := conn.Request(context.Background(), "hello", nil, &out); err != nil || !out.Hi {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	select {
	case n := <-notes:
		if n.Method != "note" {
			t.Fatalf("notification=%+v", n)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("notification not delivered over WebSocket")
	}
}

func TestDialWebSocketFailureIsTransportError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	_, err := DialWebSocket(context.Background(), server.URL+"/ws", Handlers{})
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("error=%v", err)
	}
}
