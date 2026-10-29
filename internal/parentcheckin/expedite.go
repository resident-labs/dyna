package parentcheckin

import (
	"context"
	"slices"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/notify"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

// TurnEndParents returns the parents whose child ended its turn with open work
// and nothing in flight, so the parent's check-in is due now. A child that is
// still waiting on a job, shell, open PR, held PR, question or running child
// keeps its parent on the room interval.
func TurnEndParents(ctx context.Context, n *notify.Service, ended []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ended) == 0 {
		return out, nil
	}
	live, err := n.Sessions.Store.ListSnapshot(false)
	if err != nil {
		return nil, err
	}
	running, err := jobs.Store{DataDir: store.DataDir()}.Running(ctx)
	if err != nil {
		return nil, err
	}
	listed, err := shells.DefaultStore().List(false)
	if err != nil {
		return nil, err
	}
	prs, err := prstate.Read(prstate.Root(store.DataDir()))
	if err != nil {
		return nil, err
	}
	// inFlight names the sessions that own a running job or shell, or an open PR
	// they author or hold.
	inFlight := map[string]bool{}
	for _, job := range running {
		inFlight[job.Session] = true
	}
	for _, sh := range listed {
		if sh.Status == "running" {
			inFlight[sh.Owner] = true
		}
	}
	for _, pr := range prs.Records {
		if !pr.ClosedAt.IsZero() || pr.State == prstate.PRClosed || pr.State == prstate.PRMerged {
			continue
		}
		inFlight[pr.Author] = true
		if pr.Hold != nil {
			inFlight[pr.Hold.By] = true
		}
	}
	sessions := map[string]subagent.Info{}
	for _, s := range live {
		sessions[s.ID] = s
	}
	groups := children(live)
	g := goals.DefaultStore()
	for _, id := range ended {
		child, ok := sessions[id]
		if !ok || child.Status != subagent.StatusIdle || inFlight[child.ID] {
			continue
		}
		if slices.ContainsFunc(groups[child.ID], busyChild) {
			continue
		}
		asked, err := n.QuestionStore().OpenAskedBy(child.RoomID, child.ID)
		if err != nil {
			return nil, err
		}
		if len(asked) > 0 {
			continue
		}
		held, err := g.ListOpen(child.RoomID)
		if err != nil {
			return nil, err
		}
		items, err := g.OpenItemsAssignedTo(child.RoomID, child.ID)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 && !slices.ContainsFunc(held, func(goal goals.Goal) bool { return goal.HeldBy(child.ID) }) {
			continue
		}
		out[child.ParentSession] = true
	}
	return out, nil
}

// busyChild reports whether a child session is still working or queued.
func busyChild(s subagent.Info) bool {
	return s.Status == subagent.StatusRunning || s.Status == subagent.StatusQueued
}
