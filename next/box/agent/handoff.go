package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/contract"
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

// handoffStart gives a harness's first prompt the history of the conversation it takes over, as agy does for itself. It writes the
// generation to marker just before that first start, so a restart after it opens no second conversation.
type handoffStart struct {
	boxHarness
	history, gen, marker string
	mu                   sync.Mutex
	done                 bool
}

func (h *handoffStart) Start(ctx context.Context, turn contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	if !h.done {
		if err := os.WriteFile(h.marker, []byte(h.gen), 0600); err != nil {
			h.mu.Unlock()
			return contract.TurnEnded{}, err
		}
		h.done = true
		if len(inputs) > 0 {
			inputs = append([]contract.TurnInput(nil), inputs...)
			inputs[0].Text = handoff.Prompt(h.history, inputs[0].Text)
		}
	}
	h.mu.Unlock()
	return h.boxHarness.Start(ctx, turn, inputs, accepted)
}
