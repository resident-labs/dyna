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
	entries, _ := os.ReadDir(h.cfg.SessionDir)
	type numbered struct {
		n    int
		path string
	}
	var found []numbered
	for _, entry := range entries {
		if m := mirrorName.FindStringSubmatch(entry.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
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

// handoffDelivered reports whether the mirror records a prompt that carried the handoff, which agy accepted.
func handoffDelivered(path, gen string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	for {
		line, err := reader.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"handoff":true`)) {
			var entry struct {
				Type    string
				Message struct {
					Role       string
					Handoff    bool
					HandoffGen string
				}
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "message" && entry.Message.Role == "user" && entry.Message.Handoff && entry.Message.HandoffGen == gen {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
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

// recordInputs writes what agy was sent. The first input is marked when its prompt carried the handoff: the text recorded is the user's own.
func (h *Harness) recordInputs(inputs []contract.TurnInput, handoff bool) error {
	h.mu.Lock()
	gen := h.gen
	h.mu.Unlock()
	for i, input := range inputs {
		entry := map[string]any{
			"role":       "user",
			"deliveryId": strconv.FormatInt(int64(input.Delivery), 10),
			"content":    []map[string]any{{"type": "text", "text": input.Text}},
		}
		if handoff && i == 0 {
			entry["handoff"], entry["handoffGen"] = true, gen
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

// handoffAccepted forgets the handoff once agy has taken the prompt that carried it, and records that in the session file.
func (h *Harness) handoffAccepted() {
	h.mu.Lock()
	h.handoff = ""
	h.supplied = ""
	id, gen := h.sessionID, h.gen
	h.mu.Unlock()
	h.fm.Lock()
	mirror := h.mirror
	h.fm.Unlock()
	if err := writeMeta(h.cfg.SessionDir, sessionMeta{SessionID: id, Mirror: filepath.Base(mirror), Gen: gen}); err != nil {
		log.Printf("agy: record the handoff as delivered: %v", err)
	}
}
