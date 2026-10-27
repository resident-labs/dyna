package engine

import (
	"context"
	"log"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

// computerReminderTick is how often the engine looks at open computers. The
// reminder interval is a setting; this only bounds how late one can be.
const computerReminderTick = 15 * time.Second

// computerStatusWait bounds one status call to a computer's control socket.
const computerStatusWait = 300 * time.Millisecond

// computerReminders reminds a session to look at its open computer and to stop
// it when it is done. A running turn gets a reminder every interval. An idle
// session gets one, which wakes it, and no more until it acts on the computer or
// starts a new turn.
type computerReminders struct {
	now   func() time.Time
	every func() time.Duration
	// sessions lists the open sessions; computer reads one session's computer,
	// and reports false when it has none that answers.
	sessions func() ([]subagent.Info, error)
	computer func(ctx context.Context, id string) (computers.Status, bool)
	post     func(info subagent.Info, apps []string) error

	seen map[string]*reminderState
}

// reminderState is what the pass remembers about one session's computer.
type reminderState struct {
	// since is when the session was last reminded, or when its current stretch
	// began: first seen open, a turn started or ended, or it acted on the screen.
	since time.Time
	// idle is true while the session has no turn, and idleSent that its one
	// reminder for this stretch went out.
	idle, idleSent bool
	turns          int
	acted          time.Time
}

// pass sends the reminders that are due now. It forgets every session whose
// computer no longer needs one.
func (r *computerReminders) pass(ctx context.Context) {
	every := r.every()
	if every <= 0 {
		clear(r.seen)
		return
	}
	infos, err := r.sessions()
	if err != nil {
		log.Printf("computer reminders: %v", err)
		return
	}
	now := r.now()
	live := map[string]bool{}
	for _, info := range infos {
		idle := info.Status == subagent.StatusIdle
		if info.Status != subagent.StatusRunning && !idle {
			continue
		}
		c, ok := r.computer(ctx, info.ID)
		// A computer with keep on stays open on purpose, and one with a viewer
		// has a person watching it.
		if !ok || c.State != computers.StateRunning || c.Keep || c.Viewers > 0 {
			continue
		}
		live[info.ID] = true
		acted := c.ShotCapturedAt
		if c.LatestStatus != nil && c.LatestStatus.At.After(acted) {
			acted = c.LatestStatus.At
		}
		st := r.seen[info.ID]
		if st == nil || st.idle != idle || st.turns != info.Turns || !st.acted.Equal(acted) {
			r.seen[info.ID] = &reminderState{since: now, idle: idle, turns: info.Turns, acted: acted}
			continue
		}
		if now.Sub(st.since) < every || st.idleSent {
			continue
		}
		var apps []string
		for _, a := range c.Apps {
			apps = append(apps, a.Title)
		}
		if err := r.post(info, apps); err != nil {
			log.Printf("computer reminder for %s: %v", info.ID, err)
			continue
		}
		st.since = now
		st.idleSent = idle
	}
	for id := range r.seen {
		if !live[id] {
			delete(r.seen, id)
		}
	}
}

// runComputerReminders runs the reminder pass until ctx ends.
func runComputerReminders(ctx context.Context, n *notify.Service) {
	r := &computerReminders{
		now: time.Now,
		every: func() time.Duration {
			v, err := settings.Load("")
			if err != nil || !v.ComputersEnabled {
				return 0
			}
			return time.Duration(v.ComputersReminderMinutes) * time.Minute
		},
		sessions: func() ([]subagent.Info, error) { return n.Sessions.Store.ListSnapshot(false) },
		computer: func(ctx context.Context, id string) (computers.Status, bool) {
			call, cancel := context.WithTimeout(ctx, computerStatusWait)
			defer cancel()
			s, err := computers.NewClient(id).Status(call)
			return s, err == nil
		},
		post: func(info subagent.Info, apps []string) error {
			_, err := n.Rooms.PostEventInput(room.PostEventInput{
				RoomID: info.RoomID, FreshID: true,
				Event:      room.SessionEvent{Type: room.SessionComputer, Session: info.Address(), Apps: apps},
				To:         []string{info.Address()},
				Via:        []string{"@" + info.Address()},
				SessionIDs: map[string]string{info.Address(): info.ID},
			})
			return err
		},
		seen: map[string]*reminderState{},
	}
	t := time.NewTicker(computerReminderTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.pass(ctx)
		}
	}
}
