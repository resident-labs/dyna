package workeroutput

import (
	"os"
	"strings"
	"testing"
)

// The fixture lines are captured from a live Grok 1.0.62 `grok agent stdio`
// session (2026-10-21), with tool-call and compaction records added in the
// same wire shapes the agent uses.
func acpFixture(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/acp-grok.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestAcpFixtureDecodesTranscriptToolsUsage(t *testing.T) {
	decl := &Declaration{Dialect: DialectAcpJSON}
	normalizer := NewUsageNormalizer("", nil)
	tracker := NewToolTracker(decl)
	var message, tool, compaction bool
	var sessionID string
	var usage Usage
	for _, line := range acpFixture(t) {
		r := DecodeLine(decl, line)
		if !r.Recognized() {
			t.Fatalf("unrecognized ACP notification: %s", line)
		}
		if r.SessionID != "" {
			sessionID = r.SessionID
		}
		if r.Compaction {
			compaction = true
		}
		if u, ok := normalizer.ObserveRecord(r); ok {
			usage.Add(u)
		}
		if tracker.ObserveSignals(r.Signals) {
			tool = true
		}
		for _, e := range r.Events {
			if e.Kind == "message" && e.Role == "assistant" && e.Text != "" {
				message = true
			}
			if e.Kind == "tool" && e.Name != "" {
				tool = true
			}
		}
	}
	if !message || !tool || !compaction {
		t.Fatalf("capabilities lost: message=%v tool=%v compaction=%v", message, tool, compaction)
	}
	if sessionID != "s1" {
		t.Fatalf("session id=%q", sessionID)
	}
	if usage.InputTokens != 17618 || usage.CachedTokens != 2176 || usage.OutputTokens != 384 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestAcpOutputAccumulatesAndResetsOnNewPrompt(t *testing.T) {
	acc := AcpOutput()
	chunk := func(promptID, text string) string {
		return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + text + `"}},"_meta":{"promptId":"` + promptID + `"}}}`
	}
	acc.Observe(chunk("p1", "first "))
	acc.Observe(chunk("p1", "answer"))
	if got := acc.Take("p1"); got != "first answer" {
		t.Fatalf("accumulated=%q", got)
	}
	// A queued steering prompt (p2) answers after the current work.
	acc.Observe(chunk("p2", "steered"))
	if got := acc.Take("p2"); got != "steered" {
		t.Fatalf("queued answer=%q", got)
	}
	// A late chunk from the older prompt stays in its own buffer.
	acc.Observe(chunk("p1", "late first"))
	if got := acc.Take("p2"); got != "steered" {
		t.Fatalf("late older chunk won: %q", got)
	}
	if got := acc.Take("p1"); got != "first answerlate first" {
		t.Fatalf("older buffer=%q", got)
	}
}

func TestAcpToolStatusMapsSuccessfulToCompleted(t *testing.T) {
	decl := &Declaration{Dialect: DialectAcpJSON}
	tracker := NewToolTracker(decl)
	start := DecodeLine(decl, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call","toolCallId":"c1","title":"Read","kind":"read","status":"pending"}}}`)
	tracker.ObserveSignals(start.Signals)
	if len(start.Events) != 1 || start.Events[0].State != "started" {
		t.Fatalf("start=%+v", start)
	}
	end := DecodeLine(decl, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"successful"}}}`)
	if !tracker.ObserveSignals(end.Signals) || end.Events[0].State != "completed" {
		t.Fatalf("end=%+v", end)
	}
}

func TestAcpCompactionRecords(t *testing.T) {
	decl := &Declaration{Dialect: DialectAcpJSON}
	for _, tc := range []struct {
		status     string
		state      string
		compaction bool
	}{
		{"in_progress", "started", false},
		{"completed", "completed", true},
		{"failed", "failed", false},
	} {
		r := DecodeLine(decl, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"compaction","status":"`+tc.status+`"}}}`)
		if r.Compaction != tc.compaction || len(r.Events) != 1 || r.Events[0].Kind != "status" || r.Events[0].Name != "compaction" || r.Events[0].State != tc.state {
			t.Errorf("%s: compaction=%v events=%+v", tc.status, r.Compaction, r.Events)
		}
	}
}

func TestAcpIgnoresReplayAndUnrelatedMethods(t *testing.T) {
	decl := &Declaration{Dialect: DialectAcpJSON}
	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"_x.ai/session/setup","params":{"method":"session/new","phase":"auth","sessionId":null}}`,
		`{"jsonrpc":"2.0","method":"_x.ai/queue/changed","params":{"sessionId":"s1","entries":[]}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"echo"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"model_changed"}}}`,
		`{bad`,
	} {
		r := DecodeLine(decl, line)
		if r.Recognized() {
			t.Fatalf("noise recognized: %s -> %+v", line, r.Events)
		}
	}
}

func TestAntigravityACPContextUsageDoesNotInventBilling(t *testing.T) {
	decl := &Declaration{Dialect: DialectAcpJSON}
	record := DecodeLine(decl, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"exact","update":{"sessionUpdate":"usage_update","used":1234,"size":1048576}}}`)
	if !record.Recognized() || record.ContextTokens != 1234 {
		t.Fatalf("context occupancy lost: %+v", record)
	}
	normalizer := NewUsageNormalizer("", nil)
	if usage, ok := normalizer.ObserveRecord(record); ok {
		t.Fatalf("context occupancy became billable usage: %+v", usage)
	}
}
