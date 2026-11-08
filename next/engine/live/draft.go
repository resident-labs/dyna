package live

import (
	"unicode/utf16"

	"github.com/resident-labs/dyna/contract"
)

// draftLimit bounds each part of a buffered draft. An edit past it is dropped, so viewers stop at that length.
const draftLimit = 2 << 20

// draft is the running model call's output as far as the box has sent it. It lives only in memory: it is never stored,
// and it goes only to the streams that watch its session.
type draft struct {
	turn      contract.TurnID
	head      contract.Draft // the call: its message, kind and start, without edits
	text      string
	reasoning string
}

// PublishDraft applies one edit to the session's draft and sends it to the streams watching the session. An edit of an
// earlier call of the turn is stale and dropped. event carries the edit as a TurnDraft.
func (s *Session) PublishDraft(turn contract.TurnID, edit contract.Draft, event contract.Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removed {
		return false
	}
	current := s.draft
	if current == nil || current.turn != turn || current.head.Message < edit.Message {
		head := edit
		head.Text, head.Reasoning = nil, nil
		current = &draft{turn: turn, head: head}
	} else if current.head.Message > edit.Message {
		return true
	}
	text, ok := applyEdit(current.text, edit.Text)
	if !ok {
		return true
	}
	reasoning, ok := applyEdit(current.reasoning, edit.Reasoning)
	if !ok {
		return true
	}
	current.text, current.reasoning = text, reasoning
	s.draft = current
	s.live.publishWatchers(s.id, event)
	return true
}

// ClearDraft forgets the draft of a turn that ended.
func (s *Session) ClearDraft(turn contract.TurnID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.draft != nil && s.draft.turn == turn {
		s.draft = nil
	}
}

// snapshot is the whole draft as one event body whose parts start at 0.
func (d *draft) snapshot(session contract.SessionID) contract.TurnDraft {
	body := contract.TurnDraft{Session: session, Turn: d.turn, Draft: d.head}
	if d.text != "" {
		body.Draft.Text = &contract.DraftEdit{Text: d.text}
	}
	if d.reasoning != "" {
		body.Draft.Reasoning = &contract.DraftEdit{Text: d.reasoning}
	}
	return body
}

// applyEdit replaces part from the edit's UTF-16 offset on. An offset past the end appends, and a result past the limit
// is refused.
func applyEdit(part string, edit *contract.DraftEdit) (string, bool) {
	if edit == nil {
		return part, true
	}
	at, units := len(part), 0
	for i, r := range part {
		if units >= edit.From {
			at = i
			break
		}
		units += utf16.RuneLen(r)
	}
	if at+len(edit.Text) > draftLimit {
		return part, false
	}
	return part[:at] + edit.Text, true
}

// SubscribeWatching is Subscribe for a stream that also carries the drafts of one session. The draft buffered for the
// running turn goes first, under the session's lock, so no edit can come between it and the ones after.
func (l *Live) SubscribeWatching(room contract.RoomID, capacity int, session contract.SessionID, running contract.TurnID, wrap func(contract.TurnDraft) contract.Event) (*Subscription, chan contract.Event) {
	sub := &Subscription{ch: make(chan contract.Event, capacity), watch: session}
	s, ok := l.LookupSession(session)
	if ok {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	l.mu.Lock()
	l.subscribers[sub] = room
	l.mu.Unlock()
	if ok && !s.removed && s.draft != nil && running != "" && s.draft.turn == running && capacity > 0 {
		sub.ch <- wrap(s.draft.snapshot(session))
	}
	return sub, sub.ch
}

// publishWatchers sends an event to the streams watching session, and to no other. It runs under that session's lock.
func (l *Live) publishWatchers(session contract.SessionID, event contract.Event) {
	l.mu.Lock()
	var targets []*Subscription
	for sub := range l.subscribers {
		if sub.watch == session {
			targets = append(targets, sub)
		}
	}
	l.mu.Unlock()
	l.send(targets, event)
}
