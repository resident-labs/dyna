package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
	"github.com/resident-labs/dyna/harness/agy/handoff"
)

// handoffMarker is the file of Pi's handoff record; agy keeps its own (agy-session.json).
const handoffMarker = "/box/state/handoff-gen"

// handoffHistory renders the conversation files (.jsonl) in sessions, oldest first, as the history a new native conversation starts from.
func handoffHistory(sessions string) (string, error) {
	entries, err := os.ReadDir(sessions)
	if err != nil {
		return "", err
	}
	cleared, err := agy.ClearedFiles(sessions)
	if err != nil {
		return "", err
	}
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

// Pi's handoff is a record like agy's (harness/agy/handoffstate.go): one persisted record, one function that plans the open from it
// (planOpen), and one that performs every change of it and writes it before anything acts on it (handoffStart.move). Start, a
// refusal, acceptance, /clear and a restart all go through those two; nothing edits the record in memory alone.
//
//	pending   the engine handed a generation and no prompt carries its history yet
//	sending   a prompt carrying it was built, on Carrier, sent on File; Pi's ledger has not shown it
//	accepted  Pi's ledger holds the carrier: never carried again
//	cleared   /clear dropped it: never carried again
//
// The engine gives a handoff in the hello only until the first turn output reaches it, so a later start may carry none: this record is
// what says the history is still owed. PiG answers its preflight before it writes the prompt, so no callback says the prompt was
// taken; only the ledger does.
const (
	handoffPending  = "pending"
	handoffSending  = "sending"
	handoffAccepted = "accepted"
	handoffCleared  = "cleared"
)

type handoffRecord struct {
	Gen     string               `json:"gen"`
	Status  string               `json:"status,omitempty"`
	Carrier contract.DeliverySeq `json:"carrier,omitempty"` // the input whose prompt carried the history (sending)
	File    string               `json:"file,omitempty"`    // the Pi session file it was sent on (sending)
}

func (r handoffRecord) owed() bool { return r.Status == handoffPending || r.Status == handoffSending }

// save replaces the record with an atomic rename, creating its folder.
func (r handoffRecord) save(path string) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeSetupState(path, string(data))
}

// adoptHandoff reads the record and takes a generation the hello hands over that it does not hold, as pending, before Pi opens, so no
// crash after the hello loses it. A read error other than an absent file is an error, never "no handoff".
func adoptHandoff(hello contract.BoxHelloResponse, path string) (handoffRecord, error) {
	var rec handoffRecord
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rec, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &rec); err != nil {
			return rec, err
		}
	}
	if hello.Handoff && hello.HandoffGen != rec.Gen {
		rec = handoffRecord{Gen: hello.HandoffGen, Status: handoffPending}
		return rec, rec.save(path)
	}
	return rec, nil
}

// piOpen is how Pi opens: a new conversation, or the named file; both empty continues the newest.
type piOpen struct {
	New    bool
	Resume string
}

// planOpen decides how Pi opens from the record, and the record to carry on with. It writes nothing. A file a /clear left behind is never
// continued, and a file the carrier was sent on that was never written means nothing was sent: the history is pending again.
func planOpen(rec handoffRecord, sessions string, fresh bool) (piOpen, handoffRecord, error) {
	cleared, err := agy.ClearedFiles(sessions)
	if err != nil {
		return piOpen{}, rec, err
	}
	switch rec.Status {
	case handoffPending:
		return piOpen{New: true}, rec, nil
	case handoffSending:
		if fresh {
			// The owner cleared the session since: the handoff is dropped with the rest.
			return piOpen{New: true}, handoffRecord{Gen: rec.Gen, Status: handoffCleared}, nil
		}
		if rec.File != "" && !slices.Contains(cleared, rec.File) {
			path := filepath.Join(sessions, rec.File)
			if _, err := os.Stat(path); err == nil {
				return piOpen{Resume: path}, rec, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return piOpen{}, rec, err
			}
		}
		return piOpen{New: true}, handoffRecord{Gen: rec.Gen, Status: handoffPending}, nil
	}
	if fresh {
		return piOpen{New: true}, rec, nil
	}
	if len(cleared) == 0 {
		return piOpen{}, rec, nil
	}
	current, err := newestUncleared(sessions, cleared)
	return piOpen{New: current == "", Resume: current}, rec, err
}

// newestUncleared is the newest conversation file no /clear left behind, "" when there is none.
func newestUncleared(sessions string, cleared []string) (string, error) {
	entries, err := os.ReadDir(sessions)
	if err != nil {
		return "", err
	}
	var path string
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" || slices.Contains(cleared, entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if mod := info.ModTime().UnixNano(); path == "" || mod > latest {
			path, latest = filepath.Join(sessions, entry.Name()), mod
		}
	}
	return path, nil
}

// handoffStart wraps Pi. It carries the history on the prompt while the record says it is owed, and a /clear records the files it
// leaves behind where agy's own does, so a later handoff reads none of them.
type handoffStart struct {
	boxHarness
	sessions, path string
	file           func() string // the file the harness writes now
	history        string        // what a prompt carries while the record says it is owed
	rec            handoffRecord
	mu             sync.Mutex
}

// move is the one place the record changes: written first, then taken in memory.
func (h *handoffStart) move(next handoffRecord) error {
	if err := next.save(h.path); err != nil {
		return err
	}
	h.rec = next
	if !next.owed() {
		h.history = ""
	}
	return nil
}

// observe settles a sending record whose carrier Pi's ledger holds.
func (h *handoffStart) observe(ctx context.Context) error {
	if h.rec.Status != handoffSending {
		return nil
	}
	took, err := h.boxHarness.Delivered(ctx, h.rec.Carrier)
	if err != nil || !took {
		return err
	}
	return h.move(handoffRecord{Gen: h.rec.Gen, Status: handoffAccepted})
}

// attach is for a restart: what the ledger already shows, and the history while it is owed.
func (h *handoffStart) attach(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.observe(ctx); err != nil {
		return err
	}
	if !h.rec.owed() {
		return nil
	}
	var err error
	h.history, err = handoffHistory(h.sessions)
	return err
}

func (h *handoffStart) Start(ctx context.Context, turn contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	h.mu.Lock()
	if err := h.observe(ctx); err != nil {
		h.mu.Unlock()
		return contract.TurnEnded{}, err
	}
	if h.rec.owed() && len(inputs) > 0 {
		if err := h.move(handoffRecord{Gen: h.rec.Gen, Status: handoffSending, Carrier: inputs[0].Delivery, File: filepath.Base(h.file())}); err != nil {
			h.mu.Unlock()
			return contract.TurnEnded{}, err
		}
		inputs = append([]contract.TurnInput(nil), inputs...)
		inputs[0].Text = handoff.Prompt(h.history, inputs[0].Text)
	}
	h.mu.Unlock()
	result, err := h.boxHarness.Start(ctx, turn, inputs, accepted)
	if err != nil {
		return result, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return result, h.observe(ctx)
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
	if h.rec.Gen == "" {
		return nil
	}
	return h.move(handoffRecord{Gen: h.rec.Gen, Status: handoffCleared})
}
