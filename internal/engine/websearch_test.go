package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/websvc"
)

// With no ChatGPT sign-in and no Brave key the engine names both providers in
// one line and never reaches the network.
func TestWebSearchNamesEveryProviderWhenTheOwnerSignedInToNeither(t *testing.T) {
	calls := testCalls(t, false)
	// The call is local-only, so a session reaches it through the engine's own
	// socket, as the shell MCP process does.
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	_, err := calls["WebSearch"](ctx, []json.RawMessage{json.RawMessage(`{"query":"go language","limit":3}`)})
	if err == nil {
		t.Fatal("a search with no credential answered")
	}
	message := err.Error()
	if !strings.Contains(message, "chatgpt is not signed in") || !strings.Contains(message, "brave is not signed in") {
		t.Fatalf("error = %q, want both providers named", message)
	}
	if strings.Contains(message, "\n") {
		t.Fatalf("error spans more than one line: %q", message)
	}
}
