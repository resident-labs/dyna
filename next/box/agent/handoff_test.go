package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
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
	texts   []string
	refuse  bool
	cleared int
}

func (r *startRecorder) Start(_ context.Context, _ contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	r.texts = append(r.texts, inputs[0].Text)
	if r.refuse {
		return contract.TurnEnded{}, errors.New("refused before the prompt was accepted")
	}
	accepted()
	return contract.TurnEnded{}, nil
}

func (r *startRecorder) NewConversation(context.Context) error {
	r.cleared++
	return nil
}

func markerText(t *testing.T, path string) string {
	t.Helper()
	data, _ := os.ReadFile(path)
	return string(data)
}

// The history rides on every start until one is accepted, and the generation is recorded only then.
func TestHandoffStartRecordsTheGenerationOnlyOnceTheFirstPromptIsAccepted(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gen")
	rec := &startRecorder{refuse: true}
	h := &handoffStart{boxHarness: rec, file: func() string { return "" }, sessions: t.TempDir(), history: "HISTORY", gen: "7", marker: marker, pending: true}
	inputs := []contract.TurnInput{{Text: "hello"}}
	if _, err := h.Start(t.Context(), "t", inputs, func() {}); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	if got := markerText(t, marker); got != "" {
		t.Fatalf("a refused start recorded generation %q", got)
	}
	rec.refuse = false
	for range 2 {
		if _, err := h.Start(t.Context(), "t", inputs, func() {}); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.texts) != 3 || !strings.Contains(rec.texts[1], "HISTORY") || rec.texts[2] != "hello" {
		t.Fatalf("prompts %q", rec.texts)
	}
	if inputs[0].Text != "hello" {
		t.Fatal("the caller's input was changed")
	}
	if got := markerText(t, marker); got != "7" {
		t.Fatalf("marker %q", got)
	}
	if piHandoffDue(contract.BoxHelloResponse{Handoff: true, HandoffGen: "7"}, marker) {
		t.Fatal("an accepted generation is due again on a reload")
	}
}

// A handoff with nothing to carry still records its generation at the first accepted prompt, or a reload would start over and carry
// the new conversation as history.
func TestHandoffStartWithNoHistoryStillRecordsTheGeneration(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gen")
	h := &handoffStart{boxHarness: &startRecorder{}, file: func() string { return "" }, sessions: t.TempDir(), gen: "3", marker: marker, pending: true}
	if _, err := h.Start(t.Context(), "t", []contract.TurnInput{{Text: "hi"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	if got := markerText(t, marker); got != "3" {
		t.Fatalf("marker %q", got)
	}
}

// A /clear before the first prompt drops the pending history, and the generation is recorded so a reload does not bring it back.
func TestClearDropsThePendingHandoff(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gen")
	rec := &startRecorder{}
	h := &handoffStart{boxHarness: rec, file: func() string { return "" }, sessions: t.TempDir(), history: "HISTORY", gen: "7", marker: marker, pending: true}
	if err := h.NewConversation(t.Context()); err != nil || rec.cleared != 1 {
		t.Fatalf("cleared %d: %v", rec.cleared, err)
	}
	if _, err := h.Start(t.Context(), "t", []contract.TurnInput{{Text: "after the clear"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	if rec.texts[0] != "after the clear" {
		t.Fatalf("the cleared history was sent: %q", rec.texts[0])
	}
	if got := markerText(t, marker); got != "7" {
		t.Fatalf("marker %q", got)
	}
}

// Pi's /clear leaves the earlier files behind in the record agy's /clear uses, so a switch to agy carries none of their text.
func TestPiClearThenAHandoffCarriesNothingBeforeIt(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "old-pi.jsonl", 3*time.Hour, "question before the pi clear")
	conversation(t, dir, "new-pi.jsonl", 2*time.Hour, "question after the pi clear")
	h := &handoffStart{boxHarness: &startRecorder{}, file: func() string { return filepath.Join(dir, "new-pi.jsonl") }, sessions: dir}
	if err := h.NewConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := handoffHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "after the pi clear") || strings.Contains(got, "before the pi clear") {
		t.Fatalf("history:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-pi.jsonl")); err != nil {
		t.Fatal("a transcript was deleted")
	}
}

// agy's /clear (its harness records the files it leaves behind) then a switch to Pi carries none of them, and a Pi message written
// after the clear survives whatever its file's modification time.
func TestAgyClearBoundaryKeepsPostClearPiMessages(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "pi-before.jsonl", 5*time.Hour, "pi question before the clear")
	conversation(t, dir, "agy-1.jsonl", 4*time.Hour, "agy question before the clear")
	conversation(t, dir, "agy-2.jsonl", time.Hour, "agy question after the clear")
	// A Pi file from after the clear whose modification time is older than the mirror's last write.
	conversation(t, dir, "pi-after.jsonl", 2*time.Hour, "pi question after the clear")
	if err := agy.MarkCleared(dir, "agy-2.jsonl"); err != nil {
		t.Fatal(err)
	}
	// MarkCleared left behind every file but agy-2; the Pi file written after the clear is a new file and not in the record.
	if err := os.WriteFile(filepath.Join(dir, "agy-session.json"), []byte(`{"cleared":["pi-before.jsonl","agy-1.jsonl"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := handoffHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agy question after the clear", "pi question after the clear"} {
		if !strings.Contains(got, want) {
			t.Fatalf("history lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "before the clear") {
		t.Fatalf("history carries text from before the clear:\n%s", got)
	}
}
