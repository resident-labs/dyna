package goalsvc

import (
	"errors"
	"fmt"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/subagent"
)

// GoalStatus computes one goal's mechanical status from live facts: its
// holders' sessions and turns, and their PRs.
func (s *Service) GoalStatus(roomID, id string) (goals.Status, error) {
	g, err := s.Store.Get(roomID, id)
	if err != nil {
		return goals.Status{}, err
	}
	items, err := s.Store.Items(roomID, id)
	if err != nil {
		return goals.Status{}, err
	}
	f, err := s.goalFacts(roomID, g)
	if err != nil {
		return goals.Status{}, err
	}
	return goals.ComputeStatus(g, items, f, s.now()), nil
}

func (s *Service) prState(roomID string, holders []string) (state.State, error) {
	if s.PRState != nil {
		return s.PRState()
	}
	return s.readPRState(roomID, holders)
}

// goalFacts aggregates one goal's holder facts. A running turn anywhere is
// work; errored needs the holder idle; PR facts follow any holder's PRs
// since the goal began.
func (s *Service) goalFacts(roomID string, g goals.Goal) (goals.Facts, error) {
	var f goals.Facts
	prs, err := s.prState(roomID, g.Holders)
	if err != nil {
		return f, err
	}
	for _, holder := range g.Holders {
		info, err := s.Notify.Sessions.Store.Info(holder)
		switch {
		case errors.Is(err, subagent.ErrNotFound):
			// No session record: only its session facts are unknown.
		case err != nil:
			return f, fmt.Errorf("read session %s: %w", holder, err)
		case rolesession.Busy(info):
			f.Busy = true
		case info.Turns > 0:
			turn, err := s.Notify.Sessions.Store.ReadTurn(holder, info.Turns)
			if err != nil {
				return f, fmt.Errorf("read turn %d of session %s: %w", info.Turns, holder, err)
			}
			if turn.Status == subagent.TurnError {
				f.LastFailed = true
			}
		}
		for _, pr := range prs.AuthoredBy(roomID, holder, g.CreatedAt) {
			switch pr.State {
			case state.PROpen:
				f.OpenPR = true
			case state.PRMerged:
				f.MergedPR = true
			}
		}
	}
	return f, nil
}
