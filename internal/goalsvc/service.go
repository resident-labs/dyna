// Package goalsvc provides goal operations shared by CLI and room services.
package goalsvc

import (
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

// readPRState reads only the room's PRs that the holders author or review.
func (s *Service) readPRState(roomID string, holders []string) (state.State, error) {
	return state.ReadForSessions(state.Root(store.DataDir()), roomID, holders...)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Milestone stores a quiet feed row without waking anyone. The row is a typed
// goal event, so the web reads the goal and the verdict from the payload.
func (s *Service) Milestone(g goals.Goal, by, verb string) error {
	rec, err := s.Notify.Rooms.Resolve(g.Room)
	if err != nil {
		return err
	}
	who := by
	if info, err := s.Notify.Sessions.Store.Info(by); err == nil {
		who = info.Name
	}
	change := room.GoalChange(verb)
	title := g.Text
	if change == room.GoalFinished {
		title = goals.Clip(g.Text, 60)
	}
	ev := room.GoalEvent{Type: change, GoalID: g.ID, Title: title, Actor: who, State: room.GoalState(g.State)}
	if change == room.GoalFinished && g.Done != nil {
		ev.Summary = g.Done.Summary
	}
	_, err = s.Notify.Rooms.PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: ev, To: []string{rec.Name}})
	return err
}
