package agent

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
	"github.com/resident-labs/dyna/harness/agy/handoff"
)

// handoffMarker records the handoff generation Pi was last opened for, so a restarted box does not open a second conversation for a
// handoff it already delivered. agy keeps its own record (agy.Config.HandoffGen).
const handoffMarker = "/box/state/handoff-gen"

// handoffHistory renders the conversation files (.jsonl) in sessions, oldest first, as the history a new native conversation starts from.
func handoffHistory(sessions string) (string, error) {
	entries, err := os.ReadDir(sessions)
	if err != nil {
		return "", err
	}
	type file struct {
		path string
		mod  int64
	}
	var files []file
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && filepath.Ext(entry.Name()) == ".jsonl" {
			files = append(files, file{filepath.Join(sessions, entry.Name()), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	// A /clear began the mirror agy's state names, and nothing before it is history; the files stay for the transcript.
	if cleared := agy.ClearedFrom(sessions); cleared != "" {
		if i := slices.Index(paths, filepath.Join(sessions, cleared)); i > 0 {
			paths = paths[i:]
		}
	}
	return handoff.Build(paths, 0)
}

// piHandoffDue says Pi must open a new conversation: the hello hands a conversation over and Pi has not opened one for its generation.
func piHandoffDue(hello contract.BoxHelloResponse, marker string) bool {
	if !hello.Handoff {
		return false
	}
	data, _ := os.ReadFile(marker)
	return strings.TrimSpace(string(data)) != hello.HandoffGen
}

// handoffStart gives a harness's first prompt the history of the conversation it takes over, as agy does for itself. The generation goes
// to marker only once the harness accepts that prompt, so a refused start or a restart before it delivers the history again; a /clear
// drops the history and records the generation the same way, so it is never given after the clear.
type handoffStart struct {
	boxHarness
	history, gen, marker string
	settled              bool // the generation is recorded: delivered, or dropped by a /clear
	mu                   sync.Mutex
}

func (h *handoffStart) Start(ctx context.Context, turn contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	history, open := h.history, !h.settled
	h.mu.Unlock()
	if history != "" && len(inputs) > 0 {
		inputs = append([]contract.TurnInput(nil), inputs...)
		inputs[0].Text = handoff.Prompt(history, inputs[0].Text)
	}
	return h.boxHarness.Start(ctx, turn, inputs, func() {
		if open {
			h.settle()
		}
		accepted()
	})
}

func (h *handoffStart) NewConversation(ctx context.Context) error {
	if err := h.boxHarness.NewConversation(ctx); err != nil {
		return err
	}
	h.settle()
	return nil
}

// settle drops the history and records its generation as dealt with.
func (h *handoffStart) settle() {
	h.mu.Lock()
	h.history, h.settled = "", true
	h.mu.Unlock()
	if err := os.WriteFile(h.marker, []byte(h.gen), 0600); err != nil {
		log.Printf("record the handoff generation: %v", err)
	}
}
