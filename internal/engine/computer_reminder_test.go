package engine

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/subagent"
)

// Reminders fire every interval for a running turn and once for an idle
// session, and never for a computer that is kept, watched, stopped, a root's,
// or when the setting is zero.
func TestComputerRemindersFireOnlyWhenDue(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	every := 2 * time.Minute
	run := func(id string) subagent.Info {
		return subagent.Info{Meta: subagent.Meta{ID: id}, Status: subagent.StatusRunning}
	}
	open := computers.Status{State: computers.StateRunning, Apps: []computers.App{{Title: "Chromium"}}}
	infos := map[string]subagent.Info{"a": run("a")}
	statuses := map[string]computers.Status{"a": open}
	var posted []string
	r := &computerReminders{
		now:      func() time.Time { return now },
		every:    func() time.Duration { return every },
		sessions: func() ([]subagent.Info, error) { return mapValues(infos), nil },
		computer: func(_ context.Context, id string) (computers.Status, bool) { s, ok := statuses[id]; return s, ok },
		post:     func(info subagent.Info, _ []string) error { posted = append(posted, info.ID); return nil },
		seen:     map[string]*reminderState{},
	}
	step := func(d time.Duration, want int, why string) {
		t.Helper()
		now = now.Add(d)
		r.pass(t.Context())
		if len(posted) != want {
			t.Fatalf("%s: %d reminders, want %d", why, len(posted), want)
		}
	}

	step(0, 0, "first sight only starts the clock")
	step(119*time.Second, 0, "just under the interval")
	step(time.Second, 1, "a running turn at the interval")
	step(time.Minute, 1, "the clock restarts after a reminder")
	step(time.Minute, 2, "a running turn again")

	// Acting on the screen restarts the clock.
	s := statuses["a"]
	s.ShotCapturedAt = now
	statuses["a"] = s
	step(time.Second, 2, "a screenshot restarts the clock")
	step(every, 3, "reminded again after the screenshot goes stale")

	// An idle session gets one reminder, and none until a new turn.
	idle := infos["a"]
	idle.Status = subagent.StatusIdle
	infos["a"] = idle
	step(0, 3, "turn end restarts the clock")
	step(every, 4, "an idle session is reminded once")
	step(10*every, 4, "no second idle reminder")
	// The reminder wakes the session. That turn is reminded while it runs, but
	// when it ends with no screen action the session is not woken again.
	idle.Turns, idle.Status = 1, subagent.StatusRunning
	infos["a"] = idle
	step(0, 4, "the woken turn restarts the clock")
	step(every, 5, "the woken turn is reminded while it runs")
	idle.Status = subagent.StatusIdle
	infos["a"] = idle
	step(0, 5, "the woken turn ends")
	step(10*every, 5, "no idle reminder after the woken turn")
	// Acting on the screen ends that stretch; a later idle session is reminded again.
	s = statuses["a"]
	s.ShotCapturedAt = now
	statuses["a"] = s
	step(0, 5, "acting restarts the clock")
	step(every, 6, "idle and reminded once more after acting")
	step(10*every, 6, "still once")

	// Computers that need no reminder.
	for name, mutate := range map[string]func(*computers.Status){
		"keep on":      func(s *computers.Status) { s.Keep = true },
		"viewer":       func(s *computers.Status) { s.Viewers = 1 },
		"idle-stopped": func(s *computers.Status) { s.State = computers.StateIdleStopped },
	} {
		c := open
		mutate(&c)
		statuses["a"] = c
		step(0, 6, name+" starts nothing")
		step(10*every, 6, name+" is skipped")
	}
	delete(statuses, "a")
	step(10*every, 6, "a session with no computer, as a root's")
	statuses["a"] = open
	every = 0
	step(10*every+time.Hour, 6, "zero turns reminders off")
}

func mapValues(m map[string]subagent.Info) []subagent.Info {
	out := make([]subagent.Info, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
