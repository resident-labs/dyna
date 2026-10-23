package parentcheckin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

// New connects the scheduler to the engine's session and room services.
func New(n *notify.Service) *Scheduler {
	s := &Scheduler{Root: store.DataDir()}
	s.Interval = func() (time.Duration, error) { cfg, err := settings.Load(""); return cfg.SessionCheckInInterval, err }
	s.Targets = func() ([]Target, error) {
		live, err := n.Sessions.Store.ListSnapshot(false)
		if err != nil {
			return nil, err
		}
		groups := children(live)
		var out []Target
		for _, parent := range live {
			if parent.Status != subagent.StatusClosed && len(groups[parent.ID]) > 0 {
				out = append(out, Target{parent.ID, parent.RoomID})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	s.Deliver = func(ctx context.Context, target Target, id string) (bool, error) {
		return n.Sessions.DeliverCheckIn(ctx, target.ID, func() (string, error) {
			existing, err := n.Rooms.Message(target.Room, id)
			if err == nil {
				if existing.Kind == "checkin" && existing.From == room.ParentCheckInSender {
					for _, recipient := range existing.SessionIDs {
						if recipient == target.ID {
							return existing.ID, nil
						}
					}
				}
				return "", fmt.Errorf("check-in post ID belongs to another message")
			}
			if !errors.Is(err, room.ErrPostNotFound) {
				return "", err
			}

			parent, err := n.Sessions.Store.Inspect(target.ID)
			if err != nil {
				return "", err
			}
			if parent.Status == subagent.StatusClosed {
				return "", nil
			}
			live, err := n.Sessions.Store.ListSnapshot(false)
			if err != nil {
				return "", err
			}
			kids := children(live)[parent.ID]
			if len(kids) == 0 {
				return "", nil
			}
			now := time.Now()
			if s.Now != nil {
				now = s.Now()
			}
			text, err := summary(kids, goals.DefaultStore(), store.DataDir(), n.Sessions.Store, now)
			if err != nil {
				return "", err
			}
			msg, err := n.Rooms.Post(target.Room, room.PostInput{ID: id, From: room.ParentCheckInSender, Kind: "checkin", Text: text, Wake: true, To: []string{parent.Address()}, SessionIDs: map[string]string{parent.Address(): parent.ID}})
			return msg.ID, err
		})
	}
	return s
}

func children(live []subagent.Info) map[string][]subagent.Info {
	parents := map[string]subagent.Info{}
	for _, s := range live {
		if s.Status != subagent.StatusClosed {
			parents[s.ID] = s
		}
	}
	out := map[string][]subagent.Info{}
	for _, s := range live {
		if s.Status != subagent.StatusRunning && s.Status != subagent.StatusQueued && s.Status != subagent.StatusIdle {
			continue
		}
		p, ok := parents[s.ParentSession]
		if !ok || p.RoomID != s.RoomID {
			continue
		}
		out[p.ID] = append(out[p.ID], s)
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool { return out[id][i].ID < out[id][j].ID })
	}
	return out
}

const message = "Check on your children. For each one below, confirm it is making progress. If one is stuck, idle with unfinished work, or waiting for something that already happened, nudge it, restart it, or take over its work. Close children whose work is done. If everything is on track, end your turn without posting."

type childReport struct {
	name, status, work, prs string
	since                   time.Time
}

func summary(kids []subagent.Info, g *goals.Store, root string, sessions *subagent.Store, now time.Time) (string, error) {
	prs, err := prstate.Read(prstate.Root(root))
	if err != nil {
		return "", err
	}
	var reports []childReport
	for _, child := range kids {
		var work []string
		held, err := g.ListOpen(child.RoomID)
		if err != nil {
			return "", err
		}
		for _, goal := range held {
			if goal.HeldBy(child.ID) {
				work = append(work, goal.ID+": "+goals.Clip(goal.Text, 100))
			}
		}
		items, err := g.OpenItemsAssignedTo(child.RoomID, child.ID)
		if err != nil {
			return "", err
		}
		for _, item := range items {
			work = append(work, item.Goal+"/"+item.Item.ID+": "+goals.Clip(item.Item.Text, 100))
		}
		if len(work) == 0 {
			work = append(work, "no open goal or item")
		}
		var links []string
		for _, pr := range prs.Records {
			if pr.Author == child.ID && pr.ClosedAt.IsZero() && pr.State != "closed" && pr.State != "merged" {
				links = append(links, fmt.Sprintf("open PR #%d: %s", pr.Number, pr.State))
			}
		}
		sort.Strings(links)
		if len(links) == 0 {
			links = append(links, "no open PRs")
		}
		since := child.LastActivity
		if child.Turns > 0 {
			turn, err := sessions.ReadTurn(child.ID, child.Turns)
			if err != nil {
				return "", err
			}
			switch child.Status {
			case subagent.StatusRunning:
				since = turn.Started
			case subagent.StatusQueued:
				since = turn.Created
			case subagent.StatusIdle:
				since = turn.Ended
			}
		}
		if since.IsZero() {
			since = child.LastActivity
		}
		if since.IsZero() {
			since = child.Created
		}
		reports = append(reports, childReport{child.Name, child.Status, strings.Join(work, ", "), strings.Join(links, ", "), since})
	}
	return formatSummary(reports, now), nil
}

func formatSummary(reports []childReport, now time.Time) string {
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].since.Equal(reports[j].since) {
			return reports[i].name < reports[j].name
		}
		return reports[i].since.Before(reports[j].since)
	})
	var b strings.Builder
	b.WriteString(message)
	for _, child := range reports {
		elapsed := now.Sub(child.since)
		if elapsed < 0 {
			elapsed = 0
		}
		duration := fmt.Sprintf("%dm", int64(elapsed/time.Minute))
		if elapsed < time.Minute {
			duration = "<1m"
		}
		fmt.Fprintf(&b, "\n%s · %s for %s · %s · %s", child.name, child.status, duration, child.work, child.prs)
	}
	return b.String()
}
