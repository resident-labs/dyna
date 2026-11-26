package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func conversation(t *testing.T, dir, name string, age time.Duration, question string) {
	t.Helper()
	line := fmt.Sprintf(`{"type":"message","id":%q,"message":{"role":"user","deliveryId":"1","content":[{"type":"text","text":%q}]}}`, name, question)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// The history reads only conversation files, oldest first, so agy's own record and a stray file never reach the prompt.
func TestHandoffHistoryReadsConversationFilesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "b.jsonl", time.Hour, "second question")
	conversation(t, dir, "a.jsonl", 2*time.Hour, "first question")
	if err := os.WriteFile(filepath.Join(dir, "agy-session.json"), []byte("{not a conversation"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := handoffHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first, second := strings.Index(got, "first question"), strings.Index(got, "second question"); first < 0 || second < first {
		t.Fatalf("history is not oldest first:\n%s", got)
	}
}

func TestPiHandoffDueOnlyForAGenerationItHasNotOpened(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gen")
	hello := contract.BoxHelloResponse{Handoff: true, HandoffGen: "7"}
	if !piHandoffDue(hello, marker) {
		t.Fatal("a handoff with no marker is due")
	}
	if err := os.WriteFile(marker, []byte("7"), 0o600); err != nil {
		t.Fatal(err)
	}
	if piHandoffDue(hello, marker) {
		t.Fatal("the generation Pi opened for is due again")
	}
	hello.HandoffGen = "9"
	if !piHandoffDue(hello, marker) {
		t.Fatal("a new generation is due")
	}
	if piHandoffDue(contract.BoxHelloResponse{HandoffGen: "9"}, marker) {
		t.Fatal("a hello with no handoff is due")
	}
}

type startRecorder struct {
	boxHarness
	texts []string
}

func (r *startRecorder) Start(_ context.Context, _ contract.TurnID, inputs []contract.TurnInput, _ func()) (contract.TurnEnded, error) {
	r.texts = append(r.texts, inputs[0].Text)
	return contract.TurnEnded{}, nil
}

// Only the first start carries the history, and it records the generation before it runs.
func TestHandoffStartPrefixesOnlyTheFirstPrompt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gen")
	rec := &startRecorder{}
	h := &handoffStart{boxHarness: rec, history: "HISTORY", gen: "7", marker: marker}
	inputs := []contract.TurnInput{{Text: "hello"}}
	for range 2 {
		if _, err := h.Start(t.Context(), "t", inputs, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.texts) != 2 || !strings.Contains(rec.texts[0], "HISTORY") || !strings.Contains(rec.texts[0], "hello") || rec.texts[1] != "hello" {
		t.Fatalf("prompts %q", rec.texts)
	}
	if inputs[0].Text != "hello" {
		t.Fatal("the caller's input was changed")
	}
	if data, _ := os.ReadFile(marker); string(data) != "7" {
		t.Fatalf("marker %q", data)
	}
}
