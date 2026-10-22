package goalkeeper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// GoalStatus computes one goal's mechanical status from live facts: its
// holders' sessions and turns, their PRs, and the goal's newest event.
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

func (s *Service) prState() (state.State, error) {
	if s.PRState != nil {
		return s.PRState()
	}
	return s.readPRState()
}

// goalFacts aggregates one goal's holder facts. A running turn anywhere is
// work; errored needs the holder idle; PR facts follow any holder's PRs
// since the goal began, and the newest journal event marks freshness.
func (s *Service) goalFacts(roomID string, g goals.Goal) (goals.Facts, error) {
	var f goals.Facts
	prs, err := s.prState()
	if err != nil {
		return f, err
	}
	for _, holder := range g.Holders {
		info, err := s.Notify.Sessions.Store.Info(holder)
		if err != nil {
			continue
		}
		if rolesession.Busy(info) {
			f.Busy = true
		} else if info.Turns > 0 {
			if turn, err := s.Notify.Sessions.Store.ReadTurn(holder, info.Turns); err == nil && turn.Status == subagent.TurnError {
				f.LastFailed = true
			}
		}
		for _, pr := range prs.AuthoredBy(roomID, holder, g.CreatedAt) {
			switch pr.State {
			case "open":
				f.OpenPR = true
			case "merged":
				f.MergedPR = true
			}
		}
	}
	if events, err := s.Store.Events(roomID, g.ID, 1); err == nil && len(events) > 0 {
		f.LastEventAt = events[0].At
	}
	return f, nil
}

// nudgeStaleHolders wakes one holder per stale goal, at most once per stale
// window: update your goal or finish it.
func (s *Service) nudgeStaleHolders(ctx context.Context, st goals.State, sessions []subagent.Info, now time.Time) error {
	infos := map[string]subagent.Info{}
	for _, info := range sessions {
		infos[info.ID] = info
	}
	var failures []error
	for _, r := range st.Rooms {
		for _, g := range r.Goals {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !g.Open() || g.Service || len(g.Holders) == 0 || g.WaitUntil.After(now) {
				continue
			}
			if !g.NudgedAt.IsZero() && now.Sub(g.NudgedAt) <= goals.StaleAfter {
				continue
			}
			items, err := s.Store.Items(g.Room, g.ID)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			f, err := s.goalFacts(g.Room, *g)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !goals.Stale(goals.ComputeStatus(*g, items, f, now), f, now) {
				continue
			}
			text := fmt.Sprintf("Goal %s is stale: update your progress or finish it (`dyna goal note %s \"…\"` or `dyna goal done %s \"<summary>\"`).", g.ID, g.ID, g.ID)
			delivered := false
			for _, holder := range g.Holders {
				info, ok := infos[holder]
				if !ok || info.Service || info.Role == subagent.RoleRoot || info.Status == subagent.StatusClosed || info.Status == subagent.StatusDormant {
					continue
				}
				ok, err := s.Notify.Sessions.DeliverGoal(ctx, holder, func() (string, error) {
					msg, err := s.Notify.Rooms.Post(g.Room, room.PostInput{Wake: true, From: room.GoalKeeperSender, Kind: "goal", To: []string{info.Address()}, Text: text})
					return msg.ID, err
				})
				if err != nil {
					failures = append(failures, err)
					continue
				}
				delivered = delivered || ok
			}
			// An undelivered nudge keeps its cooldown, so a paused room or a
			// locked turn retries on a later tick.
			if !delivered {
				continue
			}
			if err := s.Store.MarkNudged(g.Room, g.ID, now); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
