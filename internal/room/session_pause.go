package room

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// SessionPause is the one authoritative room hold. It never changes activity
// timestamps or a workflow's independent pause setting.
type SessionPause struct {
	RoomID        string    `json:"roomId"`
	Paused        bool      `json:"paused"`
	PausedAt      time.Time `json:"pausedAt,omitempty"`
	ResumedAt     time.Time `json:"resumedAt,omitempty"`
	Revision      uint64    `json:"revision"`
	ResumePending bool      `json:"resumePending,omitempty"`
}
type PauseSpan struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to,omitempty"`
}
type SessionPauseEvent struct {
	SessionPause
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
}

func (s *Store) SessionPause(ref string) (SessionPause, error) {
	rec, err := s.Resolve(ref)
	if err != nil {
		return SessionPause{}, err
	}
	state := rec.SessionPause
	state.RoomID = rec.ID
	return state, nil
}
func (s *Store) Paused(ref string) (bool, error) {
	state, err := s.SessionPause(ref)
	return state.Paused, err
}

// WithRoomSnapshot serializes a short commit with room post append. Callers
// acquire session metadata/gate locks first; fn must not call a room writer.
func (s *Store) WithRoomSnapshot(ctx context.Context, id string, fn func(Record) error) error {
	return s.lockRoom(ctx, id, func() error {
		rec, err := s.readRecordHeld(id)
		if err != nil {
			return err
		}
		return fn(rec)
	})
}
func pauseEvent(state SessionPause) SessionPauseEvent {
	e := SessionPauseEvent{SessionPause: state, Kind: "room.resumed", At: state.ResumedAt}
	if state.Paused {
		e.Kind, e.At = "room.paused", state.PausedAt
	}
	return e
}
func (s *Store) SessionPauseEvents(ref string, after uint64) ([]SessionPauseEvent, error) {
	state, err := s.SessionPause(ref)
	if err != nil {
		return nil, err
	}
	events, err := s.readPauseEvents(state.RoomID)
	if err != nil {
		return nil, err
	}
	if state.Revision > 0 && (len(events) == 0 || events[len(events)-1].Revision < state.Revision) {
		events = append(events, pauseEvent(state))
	}
	out := []SessionPauseEvent{}
	for _, event := range events {
		if event.Revision > after {
			out = append(out, event)
		}
	}
	return out, nil
}
func (s *Store) readPauseEvents(id string) ([]SessionPauseEvent, error) {
	dir, err := s.dir(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, "session-pause.jsonl"))
	if os.IsNotExist(err) {
		return []SessionPauseEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []SessionPauseEvent{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var e SessionPauseEvent
		if err = json.Unmarshal(scan.Bytes(), &e); err != nil {
			return nil, err
		}
		if len(out) > 0 && e.Revision <= out[len(out)-1].Revision {
			return nil, errors.New("invalid room pause event sequence")
		}
		out = append(out, e)
	}
	return out, scan.Err()
}
func (s *Store) appendPauseEvent(pause SessionPause) error {
	if pause.Revision == 0 {
		return nil
	}
	events, err := s.readPauseEvents(pause.RoomID)
	if err != nil {
		return err
	}
	if len(events) > 0 && events[len(events)-1].Revision >= pause.Revision {
		return nil
	}
	dir, err := s.dir(pause.RoomID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(pauseEvent(pause))
	if err != nil {
		return err
	}
	db, err := database.Open(filepath.Dir(s.root()))
	if err != nil {
		return err
	}
	return state.For(db).AppendSynced(context.Background(), hub.Event{}, filepath.Join(dir, "session-pause.jsonl"), raw)
}
func (s *Store) SetSessionsPaused(ctx context.Context, ref string, paused bool) (SessionPause, error) {
	rec, err := s.Resolve(ref)
	if err != nil {
		return SessionPause{}, err
	}
	var out SessionPause
	err = s.WithRoomSnapshot(ctx, rec.ID, func(rec Record) error {
		state := rec.SessionPause
		state.RoomID = rec.ID
		// Repair a missing last event before accepting another transition.
		if err := s.appendPauseEvent(state); err != nil {
			return err
		}
		if state.Paused == paused {
			out = state
			return nil
		}
		state.Paused = paused
		state.Revision++
		if paused {
			state.PausedAt = s.now()
			state.ResumePending = false
		} else {
			state.ResumedAt = s.now()
			state.ResumePending = true
		}
		rec.SessionPause = state
		if e := s.writeRecord(rec, true); e != nil {
			return e
		}
		out = state
		return s.appendPauseEvent(state)
	})
	return out, err
}
func (s *Store) CompleteSessionResume(ctx context.Context, id string, revision uint64) error {
	return s.WithRoomSnapshot(ctx, id, func(rec Record) error {
		if rec.SessionPause.Paused || rec.SessionPause.Revision != revision {
			return errors.New("room pause generation changed")
		}
		rec.SessionPause.ResumePending = false
		return s.writeRecord(rec, true)
	})
}
func (s *Store) PauseSpans(ref string, since time.Time) ([]PauseSpan, error) {
	events, err := s.SessionPauseEvents(ref, 0)
	if err != nil {
		return nil, err
	}
	out := []PauseSpan{}
	for _, event := range events {
		if event.Paused {
			out = append(out, PauseSpan{From: event.PausedAt})
		} else if len(out) > 0 {
			out[len(out)-1].To = event.ResumedAt
		}
	}
	kept := out[:0]
	for _, span := range out {
		if span.To.IsZero() || span.To.After(since) {
			if span.From.Before(since) {
				span.From = since
			}
			kept = append(kept, span)
		}
	}
	return kept, nil
}
