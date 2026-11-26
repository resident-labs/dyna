package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
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
	cleared := agy.ClearedFiles(sessions)
	type file struct {
		path string
		mod  int64
	}
	var files []file
	for _, entry := range entries {
		// A /clear, Pi's or agy's, left earlier files behind: they stay for the transcript and are no history.
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && filepath.Ext(entry.Name()) == ".jsonl" && !slices.Contains(cleared, entry.Name()) {
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

// handoffState is what Pi has done for the handoff the engine last handed it, kept in the box state so a restart finds it. The engine
// gives a handoff in the hello only until the first turn output reaches it, so a later start may carry none: this record is what says
// Pi's history is still owed.
type handoffState struct {
	Gen string `json:"gen"`
	// Pending says the history was not yet carried by a prompt. It stays so until a start writes the prompt that carries it.
	Pending bool `json:"pending,omitempty"`
	// Carrier is the input whose prompt carried the history: Pi's ledger of delivered inputs says whether it took it. PiG answers the
	// preflight before it writes the prompt, so no callback can.
	Carrier contract.DeliverySeq `json:"carrier,omitempty"`
}

func (s handoffState) save(path string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// loadHandoff reads the state at marker, and records a new generation from the hello as pending before anything else happens, so no
// crash after the hello loses it.
func loadHandoff(hello contract.BoxHelloResponse, marker string) (handoffState, error) {
	var state handoffState
	if data, err := os.ReadFile(marker); err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			return state, err
		}
	}
	if hello.Handoff && hello.HandoffGen != state.Gen {
		state = handoffState{Gen: hello.HandoffGen, Pending: true}
		return state, state.save(marker)
	}
	return state, nil
}

// owed says whether the history is still owed once Pi is open: it is when no prompt carried it, or when the one that did was never
// recorded as delivered, as after a crash between the preflight and the write.
func (s handoffState) owed(delivered func(contract.DeliverySeq) (bool, error)) (bool, error) {
	if s.Pending || s.Carrier == 0 {
		return s.Pending, nil
	}
	took, err := delivered(s.Carrier)
	return !took, err
}

// handoffStart wraps Pi. A /clear records the files it leaves behind where agy's own does, so a later handoff reads none of them. While a
// handoff is pending it also gives the first prompt the history of the conversation Pi takes over, as agy does for itself. Before that
// prompt runs, marker records the input that carries it. A /clear drops the history and records the generation alone, so it is never
// given after the clear, and fails if that cannot be recorded.
type handoffStart struct {
	boxHarness
	sessions, history, marker string
	state                     handoffState
	file                      func() string // the file the harness writes now
	mu                        sync.Mutex
}

func (h *handoffStart) Start(ctx context.Context, turn contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	if h.state.Pending && len(inputs) > 0 {
		next := handoffState{Gen: h.state.Gen, Carrier: inputs[0].Delivery}
		if err := next.save(h.marker); err != nil {
			h.mu.Unlock()
			return contract.TurnEnded{}, err
		}
		inputs = append([]contract.TurnInput(nil), inputs...)
		inputs[0].Text = handoff.Prompt(h.history, inputs[0].Text)
		h.history, h.state = "", next
	}
	h.mu.Unlock()
	return h.boxHarness.Start(ctx, turn, inputs, accepted)
}

func (h *handoffStart) NewConversation(ctx context.Context) error {
	if err := h.boxHarness.NewConversation(ctx); err != nil {
		return err
	}
	if err := agy.MarkCleared(h.sessions, filepath.Base(h.file())); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state.Pending {
		next := handoffState{Gen: h.state.Gen}
		if err := next.save(h.marker); err != nil {
			return err
		}
		h.history, h.state = "", next
	}
	return nil
}
