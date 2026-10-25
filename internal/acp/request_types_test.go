package acp

import (
	"encoding/json"
	"testing"
)

// fieldSet answers the next request of method with an empty result and reports
// its params as raw top-level fields.
func fieldSet(t *testing.T, peer *pipeAgent, method string) <-chan map[string]json.RawMessage {
	t.Helper()
	fields := make(chan map[string]json.RawMessage, 1)
	go func() {
		msg := peer.wait(t, method)
		raw, _ := json.Marshal(msg["params"])
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			decoded = nil
		}
		fields <- decoded
		peer.reply(msg["id"], map[string]any{})
	}()
	return fields
}

// TestInitializeRequestCarriesTypedCapabilities guards the handshake record:
// version, client identity, file capabilities and the extension client type
// must each reach the agent under their protocol names.
func TestInitializeRequestCarriesTypedCapabilities(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	requested := fieldSet(t, peer, "initialize")
	if _, err := Handshake(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	fields := <-requested
	want := map[string]string{
		"protocolVersion":    "1",
		"clientCapabilities": `{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}`,
		"clientInfo":         `{"name":"dyna","version":"1"}`,
		"_meta":              `{"clientType":"extension"}`,
	}
	for name, value := range want {
		if got := string(fields[name]); got != value {
			t.Errorf("initialize %s = %s, want %s", name, got, value)
		}
	}
}

// TestSessionRequestsCarryTypedParams guards the shared session request: an
// absent MCP list reaches the agent as an empty array, session/new names no
// session id, and provider meta rides along as raw JSON on every restore path.
func TestSessionRequestsCarryTypedParams(t *testing.T) {
	peer, conn := dialStdioTest(t, Handlers{})
	agent := ConnectedAgent(conn)
	agent.SessionMeta = json.RawMessage(`{"agy":{"disabledTools":["start_subagent"]}}`)
	type requested struct {
		method string
		fields map[string]json.RawMessage
	}
	requests := make(chan requested, 3)
	go func() {
		for _, method := range []string{"session/new", "session/load", "session/resume"} {
			msg := peer.wait(t, method)
			raw, _ := json.Marshal(msg["params"])
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			requests <- requested{method: method, fields: fields}
			peer.reply(msg["id"], map[string]any{"sessionId": "s-1"})
		}
	}()
	ctx := t.Context()
	if _, err := agent.NewSession(ctx, "/repo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.LoadSession(ctx, "s-1", "/repo", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ResumeSession(ctx, "s-1", "/repo", nil); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		got := <-requests
		if servers := string(got.fields["mcpServers"]); servers != "[]" {
			t.Fatalf("%s mcpServers = %s, want []", got.method, servers)
		}
		if cwd := string(got.fields["cwd"]); cwd != `"/repo"` {
			t.Errorf("%s cwd = %s, want /repo", got.method, cwd)
		}
		sessionID, named := got.fields["sessionId"]
		if named != (got.method != "session/new") {
			t.Errorf("%s sessionId = %s, named=%v", got.method, sessionID, named)
		}
		if meta := string(got.fields["_meta"]); meta != `{"agy":{"disabledTools":["start_subagent"]}}` {
			t.Errorf("%s _meta = %s", got.method, meta)
		}
	}
}
