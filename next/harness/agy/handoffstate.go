package agy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
)

// A handoff is the earlier conversation that a fresh native session starts from. It has a generation (from the engine, else a hash of
// its text) and a status, kept in agy-session.json with the session it belongs to. Every place that decides about a handoff goes
// through this file: planOpen at Open, and handoffMove for each change after it.
//
//	pending   the history waits for the first prompt that can carry it
//	sending   a prompt carrying it was built, and written to agy or about to be; agy has not answered it
//	accepted  agy took it (or there was nothing to carry): never given again
//	cleared   /clear dropped it: never given again
//
// A generation seen again is the session already opened for it, never a second one (T3 keeps one handoff record per transfer).
const (
	handoffPending  = "pending"
	handoffSending  = "sending"
	handoffAccepted = "accepted"
	handoffCleared  = "cleared"
)

// handoffOpen is what Open starts from: the session file's state and what the caller asked for.
type handoffOpen struct {
	fresh                 bool   // open a new native session instead of resuming the stored one
	text, gen, status     string // the handoff the session starts with
	supplied, suppliedGen string // the history to use if agy cannot resume, in place of one built from the mirror
	notice                string
}

// requestedGen is the generation the caller means: its own, else a hash of the text; "" when it asks for no handoff.
func requestedGen(c Config) string {
	if c.HandoffGen != "" || c.Handoff == "" {
		return c.HandoffGen
	}
	sum := sha256.Sum256([]byte(c.Handoff))
	return hex.EncodeToString(sum[:8])
}

// planOpen decides, from the stored state and the request, how Open starts. delivered says the mirror records the stored
// generation as taken by agy: a crash between the answer and the session file.
func planOpen(c Config, meta sessionMeta, delivered bool) handoffOpen {
	req := requestedGen(c)
	stored, status, text := meta.Gen, meta.Status, meta.Handoff
	if status == "" && text != "" {
		status = handoffPending
	}
	if status == handoffSending && delivered {
		status, text = handoffAccepted, ""
	}
	p := handoffOpen{fresh: c.New, gen: stored, status: status, text: text}
	switch {
	case c.New && (meta.Mirror == "" || req != stored || req == ""):
		// A new intent starts a fresh session with its own history, whatever the stored state was, uncertain or not. With no handoff
		// requested it starts blank.
		p.text, p.gen, p.status = c.Handoff, req, handoffPending
		if c.Handoff == "" {
			p.status = handoffAccepted // an explicit generation with no history still stands for the session it opened
			if req == "" {
				p.status = ""
			}
		}
	case status == handoffSending && text != "":
		// The history was on its way to agy when the last run ended: it may or may not have arrived. T3 replaces the native thread
		// (ContextHandoffDeliveryUncertainError); so does this, and gives the history again.
		p.fresh, p.status = true, handoffPending
		p.notice = "the earlier conversation may not have reached agy; a new session is opened and it is handed over again"
	default:
		p.fresh = false // the same generation again is its session
	}
	if p.status == handoffAccepted && p.text != "" || p.status == handoffCleared {
		p.text = ""
	}
	// What the caller gave stands in for the mirror when agy cannot resume, unless its generation was already taken or cleared.
	if c.Handoff != "" && !(req == stored && slices.Contains([]string{handoffAccepted, handoffCleared}, status)) {
		p.supplied, p.suppliedGen = c.Handoff, req
	}
	return p
}

// state is the session file's content for the harness as it is now.
func (h *Harness) state() sessionMeta {
	h.mu.Lock()
	meta := sessionMeta{SessionID: h.sessionID, Handoff: h.handoff, Gen: h.gen, Status: h.hstatus, ClearFrom: h.clearFrom}
	h.mu.Unlock()
	h.fm.Lock()
	meta.Mirror = filepath.Base(h.mirror)
	h.fm.Unlock()
	return meta
}

// writeState saves the session file; a failure is returned, so the caller can stop before it relies on what it could not save.
func (h *Harness) writeState() error { return writeMeta(h.cfg.SessionDir, h.state()) }

// handoffMove changes the handoff's status and saves it. Accepted also writes the mirror's line for the generation, first, so a
// crash between the two does not give the history again. Moving to accepted or cleared drops the text.
func (h *Harness) handoffMove(to string) error {
	h.mu.Lock()
	h.hstatus = to
	gen := h.gen
	if to == handoffAccepted || to == handoffCleared {
		h.handoff, h.supplied = "", ""
	}
	h.mu.Unlock()
	if to == handoffAccepted {
		line, _ := json.Marshal(map[string]any{"type": "handoff", "gen": gen})
		h.fm.Lock()
		path := h.mirror
		h.fm.Unlock()
		if file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err != nil {
			log.Printf("agy: record the handoff as delivered: %v", err)
		} else {
			file.Write(append(line, '\n'))
			file.Close()
		}
	}
	return h.writeState()
}

// markCleared records that a /clear began the current mirror file: a handoff built from the mirrors starts there.
func (h *Harness) markCleared() error {
	from := filepath.Base(h.mirrorPath())
	h.mu.Lock()
	h.clearFrom = from
	h.mu.Unlock()
	return h.writeState()
}
