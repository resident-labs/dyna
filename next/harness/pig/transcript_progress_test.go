package pig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

func TestTranscriptProgress(t *testing.T) {
	data := `{"type":"session","timestamp":"2026-11-14T00:00:00Z"}
{"type":"message","timestamp":"2026-11-14T00:00:01Z","message":{"role":"assistant","usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":4},"content":[{"type":"toolCall"},{"type":"toolCall"}]}}
{"type":"message","timestamp":"2026-11-14T00:00:02Z","message":{"role":"toolResult","nestedCalls":{"calls":[{"id":"outer/1"},{"id":"outer/2"}]},"content":[{"type":"text","text":"done"}]}}
{"type":"message","timestamp":"2026-11-14T00:00:03Z","message":{"role":"assistant","usage":{"input":5,"output":6},"content":[{"type":"text","text":"done"}]}}
`
	p, err := readProgress(strings.NewReader(data + `{"type":"message","timestamp":"2026-11-14T00:00:04Z"`))
	if err != nil {
		t.Fatal(err)
	}
	if p.ToolCalls != 4 || p.Usage.InputTokens != 15 || p.Usage.OutputTokens != 8 || p.Usage.CacheReadTokens != 3 || p.Usage.CacheWriteTokens != 4 || !p.LastActivity.Equal(time.Date(2026, 11, 14, 0, 0, 3, 0, time.UTC)) {
		t.Fatalf("%+v", p)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r := TranscriptReader{Dir: dir, Acked: map[string]int64{"worker.jsonl": int64(strings.Index(data, "{\"type\":\"message\",\"timestamp\":\"2026-11-14T00:00:02Z\""))}}
	p, err = r.Progress("worker.jsonl")
	if err != nil || p.ToolCalls != 2 || p.Usage.InputTokens != 10 {
		t.Fatalf("acked: %+v %v", p, err)
	}
	p, err = r.Progress("not-arrived.jsonl")
	if err != nil || p.ToolCalls != 0 || !p.LastActivity.IsZero() {
		t.Fatalf("missing: %+v %v", p, err)
	}
}

func TestTranscriptProgressPiGNestedCalls(t *testing.T) {
	message := ai.ToolResultMessage{ToolName: "codemode", NestedCalls: &ai.NestedToolCalls{Calls: []ai.NestedToolCallRecord{{ID: "outer/1"}, {ID: "outer/2"}}, Complete: true}, Details: map[string]any{"calls": []any{"unrelated"}}}
	data, err := json.Marshal(struct {
		Type      string               `json:"type"`
		Timestamp string               `json:"timestamp"`
		Message   ai.ToolResultMessage `json:"message"`
	}{"message", "2026-11-14T00:00:02Z", message})
	if err != nil {
		t.Fatal(err)
	}
	progress, err := readProgress(strings.NewReader(string(data) + "\n"))
	if err != nil || progress.ToolCalls != 2 {
		t.Fatalf("PiG tool result: %+v %v; data %s", progress, err, data)
	}
}
