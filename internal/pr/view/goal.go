package view

import (
	"context"
	"path/filepath"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/subagent"
)

func (s *Service) goalStore() *goals.Store {
	return &goals.Store{Root: filepath.Join(s.dataDir(), "goals")}
}

// SetPRGoal links a change to an open goal of its room, or clears the link
// when goalID is empty. The author, the owner and root may change it.
func (s *Service) SetPRGoal(ctx context.Context, roomRef, key, goalID string) error {
	p, r, err := s.authorPRInRoom(ctx, "SetPRGoal", roomRef, key)
	if err != nil {
		return err
	}
	if goalID != "" {
		if err := s.goalStore().CheckLinkable(r.RoomID, goalID); err != nil {
			return err
		}
	}
	return p.SetGoal(key, goalID)
}

// checkGoalLink refuses an explicit goal that cannot link, before any work.
func (s *Service) checkGoalLink(roomID, goalID string) error {
	if goalID == "" {
		return nil
	}
	return s.goalStore().CheckLinkable(roomID, goalID)
}

// linkGoal records the goal a change serves. An explicit goal replaces any
// link. Without one, a new change takes the acting session's goal, or its
// nearest ancestor's, when that is unambiguous; an existing link and an
// existing change without one stay as they are.
// It returns the change's goal after the call.
func (s *Service) linkGoal(p *watch.Service, r watch.Record, actor subagent.Info, created bool, explicit string) string {
	goal := explicit
	if goal == "" {
		if !created || r.GoalID != "" || actor.ID == "" {
			return r.GoalID
		}
		goal = s.goalStore().InheritedGoal(r.RoomID, actor.ID, func(id string) string {
			info, err := s.Notify.Sessions.Store.Info(id)
			if err != nil {
				return ""
			}
			return info.ParentSession
		})
		if goal == "" {
			return r.GoalID
		}
	}
	if goal != r.GoalID && p.SetGoal(r.Key, goal) == nil {
		return goal
	}
	return r.GoalID
}
