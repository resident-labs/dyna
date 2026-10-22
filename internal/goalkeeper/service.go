// Package goalkeeper runs the goal keeper: check-ins, journal passes and
// holder-change notices.
package goalkeeper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

type Service struct {
	Store        *goals.Store
	Notify       *notify.Service
	Now          func() time.Time
	PRState      func() (state.State, error)
	SessionState func() ([]subagent.Info, error)
	Sessions     SessionBackend
}

func New() *Service {
	return &Service{Store: goals.DefaultStore(), Notify: notify.Default(), Now: time.Now}
}
func (s *Service) sessionState() ([]subagent.Info, error) {
	if s.SessionState != nil {
		return s.SessionState()
	}
	return s.Notify.Sessions.Store.ListSnapshot(false)
}

func (s *Service) readPRState() (state.State, error) {
	return state.Read(state.Root(store.DataDir()))
}

func stableID(text string) string {
	h := sha256.Sum256([]byte(text))
	return "msg_" + hex.EncodeToString(h[:8])
}

// Milestone stores a quiet feed row without waking anyone.
func (s *Service) Milestone(g goals.Goal, by, verb string) error {
	rec, err := s.Notify.Rooms.Resolve(g.Room)
	if err != nil {
		return err
	}
	who := by
	if info, err := s.Notify.Sessions.Store.Info(by); err == nil {
		who = info.Name
	}
	text := fmt.Sprintf("%s %s goal %s: %s", who, verb, g.ID, g.Text)
	if verb == "finished" && g.Done != nil {
		text = fmt.Sprintf("Goal %s %s is done: %s", g.ID, goals.Clip(g.Text, 60), g.Done.Summary)
	}
	_, err = s.Notify.Rooms.Post(rec.ID, room.PostInput{From: who, Kind: "goal", To: []string{rec.Name}, Text: text})
	return err
}
