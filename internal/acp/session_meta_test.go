package acp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSessionMetadataSurvivesNewLoadAndResume(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	agent := ConnectedAgent(conn)
	agent.SessionMeta = json.RawMessage(`{"agy":{"disabledTools":["start_subagent","schedule","run_command"]}}`)
	requests := make(chan map[string]any, 3)
	go func() {
		for _, method := range []string{"session/new", "session/load", "session/resume"} {
			msg := peer.wait(t, method)
			requests <- msg
			peer.reply(msg["id"], map[string]any{"sessionId": "exact-session", "modes": map[string]any{"currentModeId": "default", "availableModes": []any{}}})
		}
	}()
	if _, err := agent.NewSession(ctx, "/workspace", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadSession(ctx, "exact-session", "/workspace", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ResumeSession(ctx, "exact-session", "/workspace", nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		msg := <-requests
		params := msg["params"].(map[string]any)
		meta := params["_meta"].(map[string]any)["agy"].(map[string]any)
		if !reflect.DeepEqual(meta["disabledTools"], []any{"start_subagent", "schedule", "run_command"}) {
			t.Fatalf("tool policy missing from %s: %v", msg["method"], params)
		}
	}
}

func TestHandshakeOffersAntigravityProtocolVersion(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	requested := make(chan int, 1)
	go func() {
		msg := peer.wait(t, "initialize")
		params := msg["params"].(map[string]any)
		requested <- int(params["protocolVersion"].(float64))
		peer.reply(msg["id"], map[string]any{"protocolVersion": 2})
	}()
	agent, err := HandshakeVersion(ctx, conn, 2)
	if err != nil {
		t.Fatal(err)
	}
	if version := <-requested; version != 2 || agent.Init.ProtocolVersion != 2 {
		t.Fatalf("version offered %d, accepted %d", version, agent.Init.ProtocolVersion)
	}
	var setup SessionSetup
	if err := json.Unmarshal([]byte(`{"sessionId":"session","modes":{"currentModeId":"default","availableModes":[]}}`), &setup); err != nil {
		t.Fatal(err)
	}
}

func TestMCPServerWireShapesIncludeRequiredArrays(t *testing.T) {
	for _, tc := range []struct {
		server McpServer
		want   string
	}{
		{McpServer{Name: "local", Command: "/tools"}, `{"name":"local","command":"/tools","args":[],"env":[]}`},
		{McpServer{Name: "remote", URL: "https://tools.example/mcp"}, `{"name":"remote","type":"http","url":"https://tools.example/mcp","headers":[]}`},
	} {
		data, err := json.Marshal(tc.server)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != tc.want {
			t.Fatalf("MCP transport shape: %s, want %s", data, tc.want)
		}
	}
}
