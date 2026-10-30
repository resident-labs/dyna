package config

import (
	"encoding/json"
	"testing"
)

func TestHarnessContractJSON(t *testing.T) {
	got, err := json.Marshal(HarnessConfig{MCP: []ManagedMCP{{Name: "tool", MCPDefinition: MCPDefinition{Command: "run"}}}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"mcp":[{"name":"tool","command":"run","enabledHarnesses":null}],"skills":null}`
	if string(got) != want {
		t.Fatalf("got %s; want %s", got, want)
	}
}
