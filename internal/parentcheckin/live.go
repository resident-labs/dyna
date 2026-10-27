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
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

var systemNow = time.Now

// RoomInterval applies one room's own check-in setting to the hook's global
// interval: a room override wins, Off returns zero and stops that room's
// check-ins. A room that no longer resolves keeps the global interval, so a
// broken read never silences every room.
func RoomInterval(rooms *room.Store) func(string, time.Duration) time.Duration {
	return func(roomID string, global time.Duration) time.Duration {
		if rooms == nil {
			return global
		}
		rec, err := rooms.Resolve(roomID)
		if err != nil {
			return global
		}
		return rec.CheckIn.Effective(global)
	}
}

// New connects the scheduler to the engine's session and room services. The
// caller sets Interval: the parent check-ins hook owns the interval now. Each
// room's own check-in setting overrides it or turns that room's check-ins off.
func New(n *notify.Service) *Scheduler {
	s := &Scheduler{Root: store.DataDir(), Now: systemNow}
	s.Interval = func() (time.Duration, error) { return DefaultInterval, nil }
	s.IntervalFor = RoomInterval(n.Rooms)
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
				if ev, ok := existing.Checkin(); ok && ev.Parent == target.ID {
					return existing.ID, nil
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
			ev, err := checkin(parent, kids, goals.DefaultStore(), store.DataDir(), n.Sessions.Store, now)
			if err != nil {
				return "", err
			}
			// The event wakes the parent and no other session, as the free-text
			// check-in did. It posts from the engine, and names the scheduler as
			// its producer, so no invented sender reaches the conversation.
			msg, err := n.Rooms.PostEventInput(room.PostEventInput{
				RoomID: target.Room, Event: ev, ID: id, Producer: room.ProducerParentCheckin,
				To: []string{parent.Address()}, SessionIDs: map[string]string{parent.Address(): parent.ID},
			})
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

type childReport struct {
	name, work, prs string
	state           room.CheckinState
	age             string
	since           time.Time
}

// checkin builds the typed check-in of one parent: the stalest children first,
// with the counts the card shows and the child table the agent reads.
func checkin(parent subagent.Info, kids []subagent.Info, g *goals.Store, root string, sessions *subagent.Store, now time.Time) (room.CheckinEvent, error) {
	reports, err := readReports(kids, g, root, sessions, now)
	if err != nil {
		return room.CheckinEvent{}, err
	}
	return checkinEvent(parent, reports, now), nil
}

// checkinEvent builds the event from the child reports: the stalest first,
// with the counts the card shows and the table the agent reads.
func checkinEvent(parent subagent.Info, reports []childReport, now time.Time) room.CheckinEvent {
	reports = stalestFirst(reports, now)
	ev := room.CheckinEvent{Parent: parent.ID, WindowStart: now.UnixMilli()}
	var lines []string
	for _, child := range reports {
		switch child.state {
		case room.CheckinRunning:
			ev.Counts.Running++
		case room.CheckinQueued:
			ev.Counts.Queued++
		case room.CheckinIdle:
			ev.Counts.Idle++
		}
		ev.Children = append(ev.Children, room.CheckinChild{Name: child.name, State: child.state, Age: child.age, Goal: child.work, PR: child.prs})
		lines = append(lines, fmt.Sprintf("%s · %s for %s · %s · %s", child.name, child.state, child.age, child.work, child.prs))
	}
	ev.Detail = strings.Join(lines, "\n")
	return ev
}

// readReports reads what each child is doing and how long it has been doing it.
func readReports(kids []subagent.Info, g *goals.Store, root string, sessions *subagent.Store, now time.Time) ([]childReport, error) {
	prs, err := prstate.Read(prstate.Root(root))
	if err != nil {
		return nil, err
	}
	var reports []childReport
	for _, child := range kids {
		state, ok := checkinState(child.Status)
		if !ok {
			continue
		}
		var work []string
		held, err := g.ListOpen(child.RoomID)
		if err != nil {
			return nil, err
		}
		for _, goal := range held {
			if goal.HeldBy(child.ID) {
				work = append(work, goal.ID+": "+goals.Clip(goal.Text, 100))
			}
		}
		items, err := g.OpenItemsAssignedTo(child.RoomID, child.ID)
		if err != nil {
			return nil, err
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
				links = append(links, openChangeLink(*pr))
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
				return nil, err
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
		reports = append(reports, childReport{name: child.Name, state: state, work: strings.Join(work, ", "), prs: strings.Join(links, ", "), since: since})
	}
	return stalestFirst(reports, now), nil
}

// openChangeLink names an open change for the check-in. A local change has no
// PR number, so it shows its title and change ID.
func openChangeLink(pr prstate.Record) string {
	if pr.Kind == "local" {
		id := pr.ChangeID
		if id == "" {
			id = pr.Key
		}
		return fmt.Sprintf("open local change %q (%s): %s", pr.Title, id, pr.State)
	}
	return fmt.Sprintf("open PR #%d: %s", pr.Number, pr.State)
}

// checkinState maps a session state to the state the card names.
func checkinState(status string) (room.CheckinState, bool) {
	switch status {
	case subagent.StatusRunning:
		return room.CheckinRunning, true
	case subagent.StatusQueued:
		return room.CheckinQueued, true
	case subagent.StatusIdle:
		return room.CheckinIdle, true
	}
	return "", false
}

// stalestFirst orders the children by how long they have been in their state,
// and fills in that age, so a parent reads the child most likely to be stuck
// first.
func stalestFirst(reports []childReport, now time.Time) []childReport {
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].since.Equal(reports[j].since) {
			return reports[i].name < reports[j].name
		}
		return reports[i].since.Before(reports[j].since)
	})
	for i := range reports {
		elapsed := now.Sub(reports[i].since)
		if elapsed < 0 {
			elapsed = 0
		}
		reports[i].age = fmt.Sprintf("%dm", int64(elapsed/time.Minute))
		if elapsed < time.Minute {
			reports[i].age = "<1m"
		}
	}
	return reports
}
