package pig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// NewConversation starts a fresh PiG session file while leaving the old file on disk.
// The next turn sends no prior conversation messages, keeps instructions and context sections,
// and reopening the harness resumes the newer session file.
func TestNewConversationStartsFreshAndKeepsOldFile(t *testing.T) {
	model := pigtest.NewModel(
		[]pigtest.Block{{Text: "remembered alpha"}},
		[]pigtest.Block{{Text: "fresh beta"}},
		[]pigtest.Block{{Text: "resumed gamma"}},
	)
	c := config(t, model, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h, err := Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SetPromptSections(ctx, []contract.ContextSection{{Key: "dyna_goal", Text: "Goal: keep this"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(ctx, "turn_1", []contract.TurnInput{{Delivery: 1, Text: "secret code: pineapple"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	oldFile := h.SessionFile()
	oldBytes, err := os.ReadFile(oldFile)
	if err != nil || !strings.Contains(string(oldBytes), "pineapple") {
		t.Fatalf("old session file missing turn 1: %v", err)
	}

	// Concurrent SessionFile reads during NewConversation must be race-free (go test -race).
	stopRead := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stopRead:
				return
			default:
				_ = h.SessionFile()
			}
		}
	}()
	if err := h.NewConversation(ctx); err != nil {
		close(stopRead)
		<-readerDone
		t.Fatal(err)
	}
	close(stopRead)
	<-readerDone
	newFile := h.SessionFile()
	if newFile == oldFile {
		t.Fatalf("expected a new session file path, still got %s", newFile)
	}
	if err := h.SetPromptSections(ctx, []contract.ContextSection{{Key: "dyna_goal", Text: "Goal: keep this"}}); err != nil {
		t.Fatal(err)
	}
	res, err := h.Start(ctx, "turn_2", []contract.TurnInput{{Delivery: 2, Text: "what is the secret code?"}}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != contract.TurnCompleted || res.Final != "fresh beta" {
		t.Fatalf("turn 2 result: %+v", res)
	}

	reqs := model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 model requests, got %d", len(reqs))
	}
	req2 := reqs[1]
	if len(req2.Messages) != 1 {
		t.Fatalf("turn 2 should see only its own input, got %d messages: %s", len(req2.Messages), req2.Messages)
	}
	if strings.Contains(string(req2.Messages[0]), "pineapple") || strings.Contains(string(req2.Messages[0]), "remembered alpha") {
		t.Fatalf("turn 2 leaked prior conversation: %s", req2.Messages[0])
	}
	if !strings.Contains(string(req2.System), "You are ses_test.") || !strings.Contains(string(req2.System), "Goal: keep this") {
		t.Fatalf("turn 2 lost instructions or context sections: %s", req2.System)
	}

	// Old file stays intact on disk alongside the new file.
	afterOldBytes, err := os.ReadFile(oldFile)
	if err != nil || string(afterOldBytes) != string(oldBytes) {
		t.Fatalf("old session file changed or missing: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(c.SessionDir, "*.jsonl"))
	if err != nil || len(files) != 2 {
		t.Fatalf("expected 2 jsonl files, got %v (%v)", files, err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening without New resumes the newer session file, not the cleared one.
	h2, err := Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if h2.SessionFile() != newFile {
		t.Fatalf("reopened %s, want %s", h2.SessionFile(), newFile)
	}
	if _, err := h2.Start(ctx, "turn_3", []contract.TurnInput{{Delivery: 3, Text: "still fresh?"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	req3 := model.Requests()[2]
	for _, msg := range req3.Messages {
		if strings.Contains(string(msg), "pineapple") {
			t.Fatalf("reopened session leaked cleared history: %s", msg)
		}
	}
}

// Calling NewConversation when the session is already empty (e.g., opened with Config.New) is a no-op
// and does not abandon the fresh session file.
func TestNewConversationOnFreshSessionIsNoOp(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "ok"}})
	c := config(t, model, "")
	c.New = true
	ctx := context.Background()
	h, err := Open(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	first := h.SessionFile()
	if err := h.NewConversation(ctx); err != nil {
		t.Fatal(err)
	}
	if h.SessionFile() != first {
		t.Fatalf("session file changed on empty session: %s -> %s", first, h.SessionFile())
	}
}

// Resume continues the named session file in place of the newest one in the folder.
func TestOpenResumesTheNamedFileNotTheNewest(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "first"}}, []pigtest.Block{{Text: "second"}})
	c := config(t, model, "")
	c.New = true
	first, err := Open(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Start(t.Context(), "a", []contract.TurnInput{{Delivery: 1, Text: "in the first file"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	firstPath := first.SessionFile()
	first.Close()
	second, err := Open(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Start(t.Context(), "b", []contract.TurnInput{{Delivery: 2, Text: "in the second file"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	second.Close()
	c.New, c.Resume = false, firstPath
	resumed, err := Open(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.SessionFile() != firstPath {
		t.Fatalf("resumed %s, want %s", resumed.SessionFile(), firstPath)
	}
}
