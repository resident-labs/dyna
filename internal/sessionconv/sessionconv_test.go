package sessionconv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piFixture writes the given Pi session lines and returns the file path.
func piFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readClaudeEntries(t *testing.T, path string) []claudeEntry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []claudeEntry
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry claudeEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("claude entry: %v: %s", err, line)
		}
		entries = append(entries, entry)
	}
	return entries
}

func TestPiConversationConvertsToAChainedClaudeSession(t *testing.T) {
	path := piFixture(t,
		`{"type":"session","version":3,"id":"pi-1","timestamp":"2026-10-25T10:00:00Z","cwd":"/work"}`,
		`{"type":"message","id":"s1","parentId":null,"timestamp":"2026-10-25T10:00:00Z","message":{"role":"system","content":""}}`,
		`{"type":"message","id":"u1","parentId":"s1","timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"keep the decoder work"}]}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-25T10:00:02Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private","thinkingSignature":"sig"},{"type":"text","text":"Reading the parser."},{"type":"toolCall","id":"call_pi_1","name":"read","arguments":{"path":"parser.go"}}]}}`,
		`{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-10-25T10:00:03Z","message":{"role":"toolResult","toolCallId":"call_pi_1","toolName":"read","content":[{"type":"text","text":"package parser"}],"isError":false}}`,
		`{"type":"message","id":"a2","parentId":"t1","timestamp":"2026-10-25T10:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"The parser looks correct."}]}}`,
	)
	conversation, err := ReadPi(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation.Messages) != 4 {
		t.Fatalf("messages = %d: %+v", len(conversation.Messages), conversation.Messages)
	}
	assistant := conversation.Messages[1]
	if len(assistant.Blocks) != 2 || assistant.Blocks[0].Text != "Reading the parser." || assistant.Blocks[1].Call == nil || assistant.Blocks[1].Call.ID != "call_pi_1" {
		t.Fatalf("assistant conversion lost text or call: %+v", assistant)
	}

	dir := t.TempDir()
	claudePath, err := WriteClaude(dir, "/work", "11111111-2222-4333-8444-555555555555", "claude-opus-5-5", conversation)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(dir, "projects", "-work")
	if filepath.Dir(claudePath) != wantDir {
		t.Fatalf("claude path = %s, want under %s", claudePath, wantDir)
	}
	entries := readClaudeEntries(t, claudePath)
	if len(entries) != 4 {
		t.Fatalf("claude entries = %d: %+v", len(entries), entries)
	}
	if entries[0].ParentUUID != nil || entries[0].Type != "user" || entries[0].SessionID != "11111111-2222-4333-8444-555555555555" || entries[0].Cwd != "/work" {
		t.Fatalf("first claude entry = %+v", entries[0])
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ParentUUID == nil || *entries[i].ParentUUID != entries[i-1].UUID {
			t.Fatalf("entry %d is not chained: %+v", i, entries[i])
		}
	}
	call := entries[1].Message.Content[1]
	if call.Type != "tool_use" || call.Name != "read" || call.ID == "call_pi_1" {
		t.Fatalf("tool call = %+v", call)
	}
	result := entries[2].Message.Content[0]
	if result.Type != "tool_result" || result.ToolUseID != call.ID || result.Content[0].Text != "package parser" {
		t.Fatalf("tool result = %+v", result)
	}
	for _, entry := range entries {
		for _, block := range entry.Message.Content {
			if block.Type == "thinking" || strings.Contains(block.Text, "private") {
				t.Fatalf("thinking crossed the boundary: %+v", entry)
			}
		}
	}
}

func TestPiCompactionAndContextEditNarrowTheConvertedHistory(t *testing.T) {
	path := piFixture(t,
		`{"type":"session","version":3,"id":"pi-2","timestamp":"2026-10-25T10:00:00Z","cwd":"/work"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":"a long beginning"}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-25T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}`,
		`{"type":"compaction","id":"c1","parentId":"a1","timestamp":"2026-10-25T10:01:00Z","summary":"The task is the decoder.","firstKeptEntryId":"u2"}`,
		`{"type":"message","id":"u2","parentId":"c1","timestamp":"2026-10-25T10:01:01Z","message":{"role":"user","content":"continue the decoder"}}`,
		`{"type":"context_edit","id":"e1","parentId":"u2","timestamp":"2026-10-25T10:01:02Z","targetId":"u2","replacement":{"content":[{"type":"text","text":"continue the decoder, tests only"}]}}`,
		`{"type":"model_change","id":"m1","parentId":"e1","timestamp":"2026-10-25T10:01:02Z","provider":"openai","modelId":"gpt-4o"}`,
		`{"type":"message","id":"a2","parentId":"m1","timestamp":"2026-10-25T10:01:03Z","message":{"role":"assistant","content":[{"type":"text","text":"running the tests"}]}}`,
		`{"type":"message","id":"u3","parentId":"a2","timestamp":"2026-10-25T10:01:04Z","message":{"role":"user","content":"a long tool dump"}}`,
		`{"type":"context_edit","id":"e2","parentId":"u3","timestamp":"2026-10-25T10:01:05Z","targetId":"u3","replacement":null}`,
		`{"type":"message","id":"a3","parentId":"e2","timestamp":"2026-10-25T10:01:06Z","message":{"role":"assistant","content":[{"type":"text","text":"tests pass"}]}}`,
		`{"type":"usage","id":"g1","parentId":"a3","timestamp":"2026-10-25T10:01:07Z","kind":"cache_warm"}`,
	)
	conversation, err := ReadPi(path)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Summary != "The task is the decoder." {
		t.Fatalf("summary = %q", conversation.Summary)
	}
	if len(conversation.Messages) != 3 {
		t.Fatalf("kept messages = %d: %+v", len(conversation.Messages), conversation.Messages)
	}
	if got := conversation.Messages[0].Blocks[0].Text; got != "continue the decoder, tests only" {
		t.Fatalf("context edit not applied: %q", got)
	}
	if got := conversation.Messages[2].Blocks[0].Text; got != "tests pass" {
		t.Fatalf("omitted message survived: %+v", conversation.Messages[2])
	}
}

func TestPiReaderRejectsABrokenChain(t *testing.T) {
	path := piFixture(t,
		`{"type":"session","version":3,"id":"pi-3","timestamp":"2026-10-25T10:00:00Z","cwd":"/work"}`,
		`{"type":"message","id":"u1","parentId":"missing","timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":"hello"}}`,
	)
	if _, err := ReadPi(path); err == nil || !strings.Contains(err.Error(), "missing parent") {
		t.Fatalf("broken chain error = %v", err)
	}
}

// An edit on an abandoned branch never changes the active conversation, even
// when it targets an entry the active branch also contains.
func TestPiContextEditsStayOnTheActiveBranch(t *testing.T) {
	path := piFixture(t,
		`{"type":"session","version":3,"id":"pi-branch","timestamp":"2026-10-25T10:00:00Z","cwd":"/work"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":"keep this question"}}`,
		`{"type":"context_edit","id":"eb","parentId":"u1","timestamp":"2026-10-25T10:00:02Z","targetId":"u1","replacement":null}`,
		`{"type":"message","id":"ab","parentId":"eb","timestamp":"2026-10-25T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"abandoned answer"}]}}`,
		`{"type":"message","id":"u2","parentId":"u1","timestamp":"2026-10-25T10:00:04Z","message":{"role":"user","content":"the active follow-up"}}`,
		`{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-10-25T10:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"active answer"}]}}`,
	)
	conversation, err := ReadPi(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation.Messages) != 3 {
		t.Fatalf("active messages = %d: %+v", len(conversation.Messages), conversation.Messages)
	}
	if got := conversation.Messages[0].Blocks[0].Text; got != "keep this question" {
		t.Fatalf("an abandoned edit removed an active message: %q", got)
	}
	if got := conversation.Messages[2].Blocks[0].Text; got != "active answer" {
		t.Fatalf("active branch changed: %q", got)
	}
}

// Pi puts custom messages and branch summaries in model context. The converter
// keeps them as labelled user messages instead of dropping them.
func TestPiKeepsCustomMessagesAndBranchSummaries(t *testing.T) {
	path := piFixture(t,
		`{"type":"session","version":3,"id":"pi-custom","timestamp":"2026-10-25T10:00:00Z","cwd":"/work"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":"start"}}`,
		`{"type":"custom_message","id":"cm","parentId":"u1","timestamp":"2026-10-25T10:00:02Z","customType":"hook","content":[{"type":"text","text":"injected context"}],"display":false}`,
		`{"type":"branch_summary","id":"bs","parentId":"cm","timestamp":"2026-10-25T10:00:03Z","fromId":"old","summary":"explored another approach"}`,
		`{"type":"message","id":"a1","parentId":"bs","timestamp":"2026-10-25T10:00:04Z","message":{"role":"assistant","content":[{"type":"text","text":"answer"}]}}`,
	)
	conversation, err := ReadPi(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation.Messages) != 4 {
		t.Fatalf("messages = %d: %+v", len(conversation.Messages), conversation.Messages)
	}
	custom := conversation.Messages[1]
	if len(custom.Blocks) != 2 || custom.Blocks[0].Text != "[Custom message: hook]" || custom.Blocks[1].Text != "injected context" {
		t.Fatalf("custom message = %+v", custom)
	}
	summary := conversation.Messages[2]
	if len(summary.Blocks) != 1 || !strings.Contains(summary.Blocks[0].Text, "explored another approach") || !strings.Contains(summary.Blocks[0].Text, "summary of a branch") {
		t.Fatalf("branch summary = %+v", summary)
	}
}

// The newest compaction's retained range can contain an older compaction. Pi
// suppresses the older one, so its summary must not replace the newest one.
func TestPiKeepsTheNewestCompactionSummary(t *testing.T) {
	path := piFixture(t,
		`{"type":"session","version":3,"id":"pi-twice","timestamp":"2026-10-25T10:00:00Z","cwd":"/work"}`,
		`{"type":"message","id":"u1","parentId":null,"timestamp":"2026-10-25T10:00:01Z","message":{"role":"user","content":"question one"}}`,
		`{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-10-25T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"answer one"}]}}`,
		`{"type":"compaction","id":"c1","parentId":"a1","timestamp":"2026-10-25T10:01:00Z","summary":"older summary","firstKeptEntryId":"u1"}`,
		`{"type":"message","id":"u2","parentId":"c1","timestamp":"2026-10-25T10:01:01Z","message":{"role":"user","content":"question two"}}`,
		`{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-10-25T10:01:02Z","message":{"role":"assistant","content":[{"type":"text","text":"answer two"}]}}`,
		`{"type":"compaction","id":"c2","parentId":"a2","timestamp":"2026-10-25T10:02:00Z","summary":"newer summary","firstKeptEntryId":"a1"}`,
		`{"type":"message","id":"u3","parentId":"c2","timestamp":"2026-10-25T10:02:01Z","message":{"role":"user","content":"question three"}}`,
		`{"type":"message","id":"a3","parentId":"u3","timestamp":"2026-10-25T10:02:02Z","message":{"role":"assistant","content":[{"type":"text","text":"answer three"}]}}`,
	)
	conversation, err := ReadPi(path)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Summary != "newer summary" {
		t.Fatalf("summary = %q, want the newest compaction", conversation.Summary)
	}
	if len(conversation.Messages) != 5 || conversation.Messages[0].Blocks[0].Text != "answer one" {
		t.Fatalf("kept messages = %+v", conversation.Messages)
	}
}
