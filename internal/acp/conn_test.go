package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dyna-agent/internal/jsonrpc"
)

// pipeAgent is a minimal in-process ACP agent over real pipes. Everything it
// reads lands in received; everything it writes goes to the client.
type pipeAgent struct {
	mu       sync.Mutex
	out      io.Writer
	reader   *bufio.Reader
	received chan map[string]any
}

func newPipeAgent(t *testing.T) (*pipeAgent, *os.File, *os.File) {
	t.Helper()
	agentIn, clientOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientIn, agentOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	a := &pipeAgent{out: agentOut, reader: bufio.NewReader(agentIn), received: make(chan map[string]any, 16)}
	go a.loop()
	t.Cleanup(func() {
		agentIn.Close()
		agentOut.Close()
		clientIn.Close()
		clientOut.Close()
	})
	return a, clientOut, clientIn
}

func (a *pipeAgent) loop() {
	for {
		line, err := a.reader.ReadBytes('\n')
		if len(line) > 0 {
			var msg map[string]any
			if json.Unmarshal(line, &msg) == nil {
				a.received <- msg
			}
		}
		if err != nil {
			close(a.received)
			return
		}
	}
}

func (a *pipeAgent) write(msg map[string]any) {
	raw, _ := json.Marshal(msg)
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = a.out.Write(append(raw, '\n'))
}

func (a *pipeAgent) reply(id any, result any) {
	raw, _ := json.Marshal(result)
	a.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(raw)})
}

func (a *pipeAgent) replyError(id any, code int, message string) {
	a.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (a *pipeAgent) serverRequest(id any, method string, params any) {
	raw, _ := json.Marshal(params)
	a.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(raw)})
}

func (a *pipeAgent) notifyClient(method string, params any) {
	raw, _ := json.Marshal(params)
	a.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": json.RawMessage(raw)})
}

// wait eats traffic until a message with the method arrives.
func (a *pipeAgent) wait(t *testing.T, method string) map[string]any {
	t.Helper()
	for {
		select {
		case msg, ok := <-a.received:
			if !ok {
				t.Fatalf("agent input closed waiting for %s", method)
			}
			if msg["method"] == method {
				return msg
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("timeout waiting for %s", method)
		}
	}
}

func dialStdioTest(t *testing.T, handlers Handlers) (*pipeAgent, *jsonrpc.Conn) {
	t.Helper()
	agent, clientOut, clientIn := newPipeAgent(t)
	// Closing both directions ends the reader; closing only stdin would
	// leave it waiting on the agent's still-open output pipe.
	kill := func() {
		clientOut.Close()
		clientIn.Close()
	}
	conn := AttachStdio(clientOut, clientIn, nil, kill, handlers)
	t.Cleanup(func() { conn.Close() })
	return agent, conn
}

func serveHandshake(t *testing.T, agent *pipeAgent) {
	t.Helper()
	go func() {
		msg := agent.wait(t, "initialize")
		agent.reply(msg["id"], map[string]any{"protocolVersion": 1, "authMethods": []any{map[string]any{"id": "grok.com"}}})
		msg = agent.wait(t, "authenticate")
		agent.reply(msg["id"], map[string]any{})
		msg = agent.wait(t, "session/new")
		agent.reply(msg["id"], map[string]any{"sessionId": "sid-1", "models": map[string]any{"currentModelId": "grok-4.7"}})
	}()
}

func TestConnHandshakeAndSessionSetup(t *testing.T) {
	agent, conn := dialStdioTest(t, Handlers{})
	serveHandshake(t, agent)
	ctx := context.Background()
	a, err := Handshake(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Authenticate(ctx, a.DefaultAuthMethod()); err != nil {
		t.Fatal(err)
	}
	if a.DefaultAuthMethod() != "grok.com" {
		t.Fatalf("auth method=%q", a.DefaultAuthMethod())
	}
	setup, err := a.NewSession(ctx, "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if setup.SessionID != "sid-1" {
		t.Fatalf("session=%+v", setup)
	}
	if model, ok := setup.CurrentModel(); !ok || model != "grok-4.7" {
		t.Fatalf("model=%q ok=%v", model, ok)
	}
}

func TestConnDeliversNotifications(t *testing.T) {
	updates := make(chan Update, 4)
	agent, conn := dialStdioTest(t, Handlers{Update: func(u Update) { updates <- u }})
	go func() {
		msg := agent.wait(t, "initialize")
		agent.reply(msg["id"], map[string]any{"protocolVersion": 1})
	}()
	if _, err := Handshake(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	agent.notifyClient("session/update", map[string]any{"sessionId": "s", "update": map[string]any{"sessionUpdate": "agent_message_chunk"}})
	select {
	case u := <-updates:
		if u.Method != "session/update" || u.Session != "s" {
			t.Fatalf("update=%+v", u)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("notification not delivered")
	}
}

func TestConnCancelsUnhandledPermissionRequest(t *testing.T) {
	agent, conn := dialStdioTest(t, Handlers{})
	go func() {
		msg := agent.wait(t, "initialize")
		agent.reply(msg["id"], map[string]any{"protocolVersion": 1})
	}()
	if _, err := Handshake(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	agent.serverRequest(77, "session/request_permission", map[string]any{"sessionId": "s"})
	for {
		select {
		case msg, ok := <-agent.received:
			if !ok {
				t.Fatal("pipe closed before answer")
			}
			if method, _ := msg["method"].(string); method != "" {
				continue // handshake traffic
			}
			if msg["id"] != float64(77) {
				t.Fatalf("answer id=%v", msg["id"])
			}
			outcome := msg["result"]
			encoded, _ := json.Marshal(outcome)
			if want := `{"outcome":{"behavior":"cancelled"}}`; string(encoded) != want {
				t.Fatalf("answer=%s want=%s", encoded, want)
			}
			return
		case <-time.After(30 * time.Second):
			t.Fatal("no answer to the permission request")
		}
	}
}

func TestConnReportsProtocolErrors(t *testing.T) {
	agent, conn := dialStdioTest(t, Handlers{})
	go func() {
		msg := agent.wait(t, "initialize")
		agent.replyError(msg["id"], -32000, "Authentication required")
	}()
	_, err := Handshake(context.Background(), conn)
	var e *jsonrpc.Error
	if !errors.As(err, &e) || e.ErrorCode() != -32000 {
		t.Fatalf("error=%v", err)
	}
}

// TestDialWebSocketRoundTrip runs one request and one notification over a
// real WebSocket endpoint, the transport grok's agent server speaks.
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
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}
			if msg["method"] == "initialize" {
				reply, _ := json.Marshal(map[string]any{"protocolVersion": 1})
				_ = socket.WriteMessage(websocket.TextMessage, mustJSON(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": json.RawMessage(reply)}))
				_ = socket.WriteMessage(websocket.TextMessage, mustJSON(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "ws-1", "update": map[string]any{"sessionUpdate": "agent_message_chunk"}}}))
				// Keep the connection open until the client goes away, so the
				// notification is not cut off by the server closing first.
				for {
					if _, _, err := socket.ReadMessage(); err != nil {
						return
					}
				}
			}
		}
	}))
	defer server.Close()
	updates := make(chan Update, 2)
	conn, err := DialWebSocket(context.Background(), server.URL+"/ws?server-key=test-secret", Handlers{Update: func(u Update) { updates <- u }})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := Handshake(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-updates:
		if u.Session != "ws-1" {
			t.Fatalf("update=%+v", u)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("notification not delivered over WebSocket")
	}
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

func TestConnUnknownRequestIsMethodNotFoundByDefault(t *testing.T) {
	agent, _ := dialStdioTest(t, Handlers{})
	agent.serverRequest(9, "fs/read_text_file", map[string]any{})
	select {
	case msg := <-agent.received:
		errObj, _ := msg["error"].(map[string]any)
		if msg["id"] != float64(9) || errObj["code"] != float64(jsonrpc.CodeMethodNotFound) {
			t.Fatalf("answer=%v", msg)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no answer to the unknown request")
	}
}

func TestConnPermissionHandlerShapesOutcome(t *testing.T) {
	agent, _ := dialStdioTest(t, Handlers{Permission: func(_ context.Context, req PermissionRequest) PermissionResponse {
		return PermissionResponse{Behavior: "allow_once", OptionID: req.Options[0].OptionID}
	}})
	agent.serverRequest(3, "session/request_permission", map[string]any{"sessionId": "s", "options": []any{map[string]any{"kind": "allow_once", "optionId": "o1"}}})
	select {
	case msg := <-agent.received:
		encoded, _ := json.Marshal(msg["result"])
		if want := `{"outcome":{"behavior":"allow_once","optionId":"o1"}}`; string(encoded) != want {
			t.Fatalf("answer=%s want=%s", encoded, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no answer to the permission request")
	}
}
