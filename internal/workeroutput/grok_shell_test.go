package workeroutput

import (
	"os"
	"strings"
	"testing"
)

// Captured by the live Grok shell QA session ses_20261021-065701-05cd02d9.
func TestGrokMCPShell(t *testing.T) {
	raw, err := os.ReadFile("testdata/grok-shell.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fixture has %d lines", len(lines))
	}
	decl := &Declaration{Dialect: DialectGrokJSON}
	start, end := DecodeLine(decl, lines[0]), DecodeLine(decl, lines[1])
	if len(start.Events) != 1 || start.Events[0].Name != "dyna__shell" || start.Events[0].Input != `{"command":"printf grok-shell-qa","description":"Print Grok shell QA marker"}` {
		t.Fatalf("start = %+v", start.Events)
	}
	if len(end.Events) != 1 || !strings.HasPrefix(end.Events[0].Output, "Job sh_20261021-065727-517e28b3.") {
		t.Fatalf("completion = %+v", end.Events)
	}
	paired, _ := PairTools(append(start.Events, end.Events...))
	if len(paired) != 1 || paired[0].Name != "dyna__shell" || paired[0].State != "completed" {
		t.Fatalf("paired = %+v", paired)
	}
}

func TestGrokMCPError(t *testing.T) {
	// Error variant captured from ses_20261020-104048-593614fe.
	r := DecodeLine(&Declaration{Dialect: DialectGrokJSON}, `{"type":"tool_call_update","toolCallId":"shell1","status":"failed","rawOutput":{"type":"MCP","tool_name":"shell","server_name":"dyna","output":{"Error":"working directory does not exist"},"is_error":true}}`)
	if len(r.Events) != 1 || r.Events[0].Output != "working directory does not exist" || r.Events[0].State != "failed" {
		t.Fatalf("error = %+v", r.Events)
	}
}

func TestGrokMCPFallback(t *testing.T) {
	for _, raw := range []string{
		`{"type":"MCP","output":{"Future":"keep"}}`,
		`{"type":"MCP","output":{"OkayOutput":{"text":"keep"}}}`,
		`{"type":"Bash","output":"keep"}`,
	} {
		r := DecodeLine(&Declaration{Dialect: DialectGrokJSON}, `{"type":"tool_call_update","toolCallId":"fallback","status":"completed","rawOutput":`+raw+`}`)
		if len(r.Events) != 1 || r.Events[0].Output != compactJSON([]byte(raw)) {
			t.Fatalf("fallback = %+v", r.Events)
		}
	}
	for _, line := range []string{
		`{"type":"tool_call","toolName":"use_tool","rawInput":{"future":"keep"}}`,
		`{"type":"tool_call","toolName":"bash","rawInput":{"tool_name":"keep","tool_input":{}}}`,
	} {
		r := DecodeLine(&Declaration{Dialect: DialectGrokJSON}, line)
		if len(r.Events) != 1 || !strings.Contains(r.Events[0].Input, "keep") {
			t.Fatalf("input = %+v", r.Events)
		}
	}
}
