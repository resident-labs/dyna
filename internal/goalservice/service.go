// Package goalservice provides goal operations shared by CLI and room services.
package goalservice

import (
	"fmt"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
)

type Service struct {
	Store   *goals.Store
	Notify  *notify.Service
	Now     func() time.Time
	PRState func() (state.State, error)
}

func New() *Service {
	return &Service{Store: goals.DefaultStore(), Notify: notify.Default(), Now: time.Now}
}

func (s *Service) readPRState() (state.State, error) {
	return state.Read(state.Root(store.DataDir()))
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
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
