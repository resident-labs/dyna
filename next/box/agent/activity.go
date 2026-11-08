package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
)

const (
	// draftTick is how often the box sends the model output streamed since its last send, and draftBurst the amount that
	// sends it before the tick.
	draftTick  = 250 * time.Millisecond
	draftBurst = 2 << 10
	// labelEvery is the most often a label goes: the label names what the turn does, and a tool call that ran for less
	// than this is listed in the thread anyway.
	labelEvery = time.Second
	// draftEdit bounds one edit, and draftPart a part's text; output past it is not shown live.
	draftEdit = 60 << 10
	draftPart = 1 << 20
	// draftHold bounds the unfinished last word that waits for the next delta, so a secret shows only once Mask can tell it.
	draftHold = 256
)

// activity holds what the running turn does for the engine: its latest label, and the output of its current model call.
// run sends them, batched, on the turn.activity request.
type activity struct {
	mu     sync.Mutex
	latest contract.TurnActivityRequest
	draft  draft
	burst  chan struct{}
}

// draft is the current model call's output: what streamed, and what the engine holds of it.
type draft struct {
	turn      contract.TurnID
	message   int
	kind      contract.DraftKind
	started   time.Time
	announced bool // whether the engine has heard of this call
	text      part
	reasoning part
	pending   int // bytes streamed since the last send
}

type part struct {
	raw  string // as streamed
	sent string // the masked text the engine holds
}

func newActivity() *activity { return &activity{burst: make(chan struct{}, 1)} }

func (a *activity) set(turn contract.TurnID, kind contract.ActivityKind, label string) {
	label = shell.Mask(label)
	if r := []rune(label); len(r) > 80 {
		label = string(r[:79]) + "…"
	}
	a.mu.Lock()
	a.latest = contract.TurnActivityRequest{Turn: turn, Kind: kind, Label: label}
	a.mu.Unlock()
}

// output takes the harness's streamed output. A call's start replaces the draft; its deltas grow it.
func (a *activity) output(turn contract.TurnID, d pig.Draft) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !d.Started.IsZero() {
		message := 1
		if a.draft.turn == turn {
			message = a.draft.message + 1
		}
		a.draft = draft{turn: turn, message: message, kind: d.Kind, started: d.Started}
	}
	if a.draft.turn != turn || a.draft.message == 0 {
		return
	}
	for _, grow := range []struct {
		part  *part
		delta string
	}{{&a.draft.text, d.Text}, {&a.draft.reasoning, d.Reasoning}} {
		if grow.delta != "" && len(grow.part.raw)+len(grow.delta) <= draftPart {
			grow.part.raw += grow.delta
			a.draft.pending += len(grow.delta)
		}
	}
	if a.draft.pending >= draftBurst {
		select {
		case a.burst <- struct{}{}:
		default:
		}
	}
}

// run sends on each tick, and early when a burst of output waits. A label goes when it changed, at most once per
// labelEvery; a draft edit goes whenever the masked output changed. A send that fails is tried again on the next tick.
func (a *activity) run(ctx context.Context, ticks <-chan time.Time, send func(context.Context, contract.TurnActivityRequest) error) {
	var sentLabel contract.TurnActivityRequest
	var labelAt time.Time
	burst := a.burst // nil after a failed send, until the next tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			burst = a.burst
		case <-burst:
		}
		now := time.Now()
		a.mu.Lock()
		req := contract.TurnActivityRequest{Turn: a.latest.Turn}
		label := a.latest.Turn != "" && a.latest != sentLabel && now.Sub(labelAt) >= labelEvery
		if label {
			req = a.latest
		}
		text, reasoning, edit := a.draft.edits()
		if edit && (!label || a.draft.turn == req.Turn) {
			req.Turn = a.draft.turn
			req.Draft = &contract.Draft{Message: a.draft.message, Kind: a.draft.kind, Started: a.draft.started, Text: text.edit, Reasoning: reasoning.edit}
		}
		a.mu.Unlock()
		if !label && req.Draft == nil {
			continue
		}
		if send(ctx, req) != nil {
			burst = nil
			continue
		}
		if label {
			sentLabel, labelAt = req, now
		}
		if req.Draft != nil {
			a.mu.Lock()
			// The draft may have moved on while the request was out; what was sent belongs to the call it named.
			if a.draft.turn == req.Turn && a.draft.message == req.Draft.Message {
				a.draft.announced = true
				a.draft.text.sent, a.draft.reasoning.sent = text.shown, reasoning.shown
				a.draft.pending = 0
			}
			a.mu.Unlock()
		}
	}
}

// partEdit is the edit that brings the engine's copy of a part to shown, nil when it already holds it.
type partEdit struct {
	shown string
	edit  *contract.DraftEdit
}

// edits are the changes to send: whether there is one, and each part's. A new call is announced even before its first text.
func (d *draft) edits() (text, reasoning partEdit, edit bool) {
	if d.message == 0 {
		return partEdit{}, partEdit{}, false
	}
	text, reasoning = d.text.edit(), d.reasoning.edit()
	return text, reasoning, !d.announced || text.edit != nil || reasoning.edit != nil
}

// edit masks what streamed as the transcript masks it, leaving out an unfinished last word, and returns the change from
// what was sent: the text after the longest common prefix, which a mask can make shorter than what was sent.
func (p *part) edit() partEdit {
	raw := p.raw
	if cut := strings.LastIndexAny(raw, " \t\n"); len(raw)-(cut+1) <= draftHold {
		raw = raw[:cut+1]
	}
	shown := shell.Mask(raw)
	same := 0
	for same < len(shown) && same < len(p.sent) {
		r, n := utf8.DecodeRuneInString(shown[same:])
		s, m := utf8.DecodeRuneInString(p.sent[same:])
		if r != s || n != m {
			break
		}
		same += n
	}
	if same == len(shown) && same == len(p.sent) {
		return partEdit{shown: shown}
	}
	rest := shown[same:]
	if len(rest) > draftEdit {
		end := draftEdit
		for end > 0 && !utf8.RuneStart(rest[end]) {
			end--
		}
		rest = rest[:end]
	}
	return partEdit{shown: shown[:same+len(rest)], edit: &contract.DraftEdit{From: utf16Len(shown[:same]), Text: rest}}
}

// utf16Len counts s in UTF-16 code units, as a browser's string length does.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func (c *Client) TurnActivity(ctx context.Context, req contract.TurnActivityRequest) error {
	body, _ := json.Marshal(req)
	_, err := c.post(ctx, contract.MethodTurnActivity, body)
	return err
}
