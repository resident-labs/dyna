package workeroutput

import "testing"

func TestIsCompaction(t *testing.T) {
	for _, tc := range []struct {
		dialect, line string
		want          bool
	}{
		{DialectClaudeStreamJSON, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":100000}}`, true},
		{DialectClaudeStreamJSON, `{"type":"system","subtype":"init"}`, false},
		{DialectClaudeStreamJSON, `{"type":"assistant","subtype":"compact_boundary"}`, false},
		{DialectClaudeStreamJSON, `{"type":"assistant","message":{"content":[{"type":"text","text":"compact_boundary"}]}}`, false},
		{DialectClaudeStreamJSON, `{`, false},
		{DialectCodexJSONL, `{"type":"system","subtype":"compact_boundary"}`, false},
	} {
		if got := IsCompaction(&Declaration{Dialect: tc.dialect}, tc.line); got != tc.want {
			t.Errorf("%s: %v", tc.line, got)
		}
	}
	if IsCompaction(nil, `{"type":"system","subtype":"compact_boundary"}`) {
		t.Fatal("undeclared protocol accepted")
	}
}

// TestCompactionEvents checks docs/reasoning-and-compaction.md: every
// adapter reports compaction as one status shape, and only success sets
// Record.Compaction. Each line is a real protocol record.
func TestCompactionEvents(t *testing.T) {
	for _, tc := range []struct {
		dialect string
		line    string
		want    Event
	}{
		// Claude 2.1.302 records from Dyna session captures.
		{DialectClaudeStreamJSON, `{"type":"system","subtype":"status","status":"compacting","session_id":"s"}`,
			Event{Kind: "status", Name: "compaction", State: "started"}},
		{DialectClaudeStreamJSON, `{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"You've hit your session limit"}`,
			Event{Kind: "status", Name: "compaction", State: "failed", Text: "You've hit your session limit"}},
		{DialectClaudeStreamJSON, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":271357,"post_tokens":10555}}`,
			Event{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto", TokensBefore: 271357, TokensAfter: 10555}},
		// Pi 1.3.0 docs/json.md.
		{DialectPiJSON, `{"type":"compaction_start","reason":"threshold"}`,
			Event{Kind: "status", Name: "compaction", State: "started", Trigger: "auto"}},
		{DialectPiJSON, `{"type":"compaction_end","reason":"threshold","result":{"summary":"s","tokensBefore":150000,"estimatedTokensAfter":32000},"aborted":false,"willRetry":false}`,
			Event{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto", TokensBefore: 150000, TokensAfter: 32000}},
		{DialectPiJSON, `{"type":"compaction_end","reason":"manual","aborted":true}`,
			Event{Kind: "status", Name: "compaction", State: "failed", Trigger: "manual"}},
		{DialectPiJSON, `{"type":"compaction_end","reason":"overflow","aborted":false,"errorMessage":"summary failed"}`,
			Event{Kind: "status", Name: "compaction", State: "failed", Trigger: "auto", Text: "summary failed"}},
		// Grok 1.0.62 compacts by itself at its threshold.
		{DialectGrokJSON, `{"type":"auto_compact_started"}`,
			Event{Kind: "status", Name: "compaction", State: "started", Trigger: "auto"}},
		{DialectGrokJSON, `{"type":"auto_compact_completed"}`,
			Event{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto"}},
		{DialectGrokJSON, `{"type":"auto_compact_failed","message":"summary model failed"}`,
			Event{Kind: "status", Name: "compaction", State: "failed", Text: "summary model failed", Trigger: "auto"}},
		// Codex names the item in snake_case on the exec stream (0.163.0).
		{DialectCodexJSONL, `{"type":"item.started","item":{"id":"c1","type":"context_compaction"}}`,
			Event{Kind: "status", Name: "compaction", State: "started", Trigger: "auto"}},
		{DialectCodexJSONL, `{"type":"item.completed","item":{"id":"c1","type":"context_compaction"}}`,
			Event{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto"}},
		// The app-server protocol names the same item in camelCase.
		{DialectCodexJSONL, `{"type":"item.completed","item":{"id":"c2","type":"contextCompaction"}}`,
			Event{Kind: "status", Name: "compaction", State: "completed", Trigger: "auto"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			r := DecodeLine(&Declaration{Dialect: tc.dialect}, tc.line)
			tc.want.V = SchemaVersion
			if len(r.Events) != 1 || r.Events[0] != tc.want {
				t.Fatalf("events=%+v, want %+v", r.Events, tc.want)
			}
			if want := tc.want.State == "completed"; r.Compaction != want {
				t.Fatalf("compaction=%v, want %v", r.Compaction, want)
			}
		})
	}
	// A plain status record keeps its generic status event.
	r := DecodeLine(&Declaration{Dialect: DialectClaudeStreamJSON}, `{"type":"system","subtype":"status","status":null,"compact_result":"success"}`)
	if len(r.Events) != 1 || r.Events[0].Name != "status" || r.Compaction {
		t.Fatalf("plain status: %+v", r)
	}
}
