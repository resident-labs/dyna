package agy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy/acp"
)

// The mirror is a Pi-format session file, <SessionDir>/agy-<n>.jsonl, that the harness writes from agy's updates, so the
// box's transcript reader, activity and delivery ledger read an agy session as they read a Pi one. A user entry carries
// the delivery it holds and is written once agy has been sent the prompt. After a crash an input can arrive twice, and
// is never lost.

var mirrorName = regexp.MustCompile(`^agy-(\d+)\.jsonl$`)

// sessionMeta is <SessionDir>/agy-session.json: the agy conversation and the mirror file that records it.
type sessionMeta struct {
	SessionID string `json:"sessionId"`
	Mirror    string `json:"mirror"`
	// Handoff is the earlier conversation a fresh session has yet to be given: it stays until agy has accepted the prompt that carried it.
	Handoff string `json:"handoff,omitempty"`
	// Gen is the generation of the handoff the session was opened for, pending or delivered.
	Gen string `json:"handoffGen,omitempty"`
	// Status is the handoff's state (handoffstate.go). A restart that finds it sending cannot tell whether agy took the history.
	Status string `json:"handoffStatus,omitempty"`
	// ClearFrom is the mirror file a /clear began: a handoff built from the mirrors reads this file and the ones after it, never the
	// conversation before. The files stay for the transcript.
	ClearFrom string `json:"clearFrom,omitempty"`
}

func readMeta(dir string) (sessionMeta, error) {
	var meta sessionMeta
	data, err := os.ReadFile(filepath.Join(dir, "agy-session.json"))
	if errors.Is(err, os.ErrNotExist) {
		return meta, nil
	}
	if err != nil {
		return meta, err
	}
	return meta, json.Unmarshal(data, &meta)
}

func writeMeta(dir string, meta sessionMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "agy-session.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "agy-session.json"))
}

// nextMirror names the file after the newest one in dir.
func nextMirror(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	highest := 0
	for _, entry := range entries {
		if m := mirrorName.FindStringSubmatch(entry.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			highest = max(highest, n)
		}
	}
	return filepath.Join(dir, fmt.Sprintf("agy-%d.jsonl", highest+1)), nil
}

// mirrorFiles is every mirror file in the session directory, oldest first: the conversations this harness has had.
func (h *Harness) mirrorFiles() []string {
	h.mu.Lock()
	from := h.clearFrom
	h.mu.Unlock()
	first := 0
	if m := mirrorName.FindStringSubmatch(from); m != nil {
		first, _ = strconv.Atoi(m[1])
	}
	entries, _ := os.ReadDir(h.cfg.SessionDir)
	type numbered struct {
		n    int
		path string
	}
	var found []numbered
	for _, entry := range entries {
		if m := mirrorName.FindStringSubmatch(entry.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n < first {
				continue
			}
			found = append(found, numbered{n, filepath.Join(h.cfg.SessionDir, entry.Name())})
		}
	}
	slices.SortFunc(found, func(a, b numbered) int { return a.n - b.n })
	paths := make([]string, len(found))
	for i, f := range found {
		paths[i] = f.path
	}
	return paths
}

// mirrorEmpty reports a mirror that has no entry yet.
func mirrorEmpty(path string) bool {
	info, err := os.Stat(path)
	return err != nil || info.Size() == 0
}

// record appends one Pi message entry to the mirror.
func (h *Harness) record(message map[string]any) error {
	h.fm.Lock()
	path := h.mirror
	h.entries++
	id := strconv.FormatInt(h.nonce, 36) + "-" + strconv.FormatInt(h.entries, 36)
	h.fm.Unlock()
	line, err := json.Marshal(map[string]any{"type": "message", "id": id, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "message": message})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(line, '\n'))
	return errors.Join(err, file.Close())
}

// recordEnding writes how a turn that did not finish ended, as Pi writes an assistant message with stopReason "error" or "aborted".
// Recover reads it: a turn ending in one is not settled.
func (h *Harness) recordEnding(reason, message string) {
	entry := map[string]any{"role": "assistant", "content": []map[string]any{}, "stopReason": reason}
	if message != "" {
		entry["errorMessage"] = safeMessage(message)
	}
	if err := h.record(entry); err != nil {
		log.Printf("agy: record the turn's ending: %v", err)
	}
}

// recordInputs writes what agy was sent.
func (h *Harness) recordInputs(inputs []contract.TurnInput) error {
	for _, input := range inputs {
		entry := map[string]any{
			"role":       "user",
			"deliveryId": strconv.FormatInt(int64(input.Delivery), 10),
			"content":    []map[string]any{{"type": "text", "text": input.Text}},
		}
		err := h.record(entry)
		if err != nil {
			return err
		}
	}
	return nil
}

// deliveries is what the mirror has been seen to record and how far it was read; the file only grows, so each check
// reads what was appended since the last one (as pig's does).
type deliveries struct {
	mu   sync.Mutex
	path string
	read int64
	seen map[string]struct{}
}

// Delivered reports whether the mirror records the delivery.
func (h *Harness) Delivered(_ context.Context, id contract.DeliverySeq) (bool, error) {
	path := h.mirrorPath()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	d := &h.delivered
	d.mu.Lock()
	defer d.mu.Unlock()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if d.path != path || info.Size() < d.read {
		d.path, d.read, d.seen = path, 0, make(map[string]struct{})
	}
	if _, err := file.Seek(d.read, io.SeekStart); err != nil {
		return false, err
	}
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break // a line an interrupted write left unfinished is read again once whole
		}
		if err != nil {
			return false, err
		}
		d.read += int64(len(line))
		var entry struct {
			Type    string
			Message struct{ Role, DeliveryID string }
		}
		if json.Unmarshal(line, &entry) == nil && entry.Type == "message" && entry.Message.Role == "user" && entry.Message.DeliveryID != "" {
			d.seen[entry.Message.DeliveryID] = struct{}{}
		}
	}
	_, found := d.seen[strconv.FormatInt(int64(id), 10)]
	return found, nil
}

// handoffDelivered reports whether the mirror records that agy took the handoff of this generation: a line of its own, written once
// the prompt that carried it was answered, which the transcript readers pass over.
func handoffDelivered(path, gen string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"type":"handoff"`)) {
			var entry struct{ Type, Gen string }
			if json.Unmarshal(line, &entry) == nil && entry.Type == "handoff" && entry.Gen == gen {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// handoffResult settles a handoff by what agy answered the prompt that carried it (T3 ContextHandoffDelivery.ts persists the delivery as
// pending around the injection and restores it when the provider refused the prompt before consuming it):
//   - agy took it (a result, or an error of its own): the handoff is delivered, and the mirror says so;
//   - agy refused the prompt as invalid (-32600 to -32602), so it never consumed it: the handoff is pending again, for the next prompt;
//   - no answer (the process went, the connection broke): whether agy took it cannot be known. T3 replaces the native thread
//     (ContextHandoffDeliveryUncertainError), and so does this: the next Start opens a fresh session and hands the history over again.
//     The old session may have had it too, which costs tokens; it is never lost.
func (h *Harness) handoffResult(err error) {
	var rpc *acp.Error
	rejected := errors.As(err, &rpc)
	switch {
	case err != nil && !rejected:
		h.mu.Lock()
		h.broken = true // the conversation is replaced; the history stays sending, and is given to the replacement
		h.mu.Unlock()
	case rejected && rpc.Code >= -32602 && rpc.Code <= -32600:
		if err := h.handoffMove(handoffPending); err != nil {
			log.Printf("agy: restore the handoff: %v", err)
		}
	default:
		if err := h.handoffMove(handoffAccepted); err != nil {
			log.Printf("agy: record the handoff as delivered: %v", err)
		}
	}
}
