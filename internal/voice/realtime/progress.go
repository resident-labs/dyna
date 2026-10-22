package realtime

// Ported from qwen-audio-agent
// server/src/voice/announcement/progress-announcement-manager.mjs
// (Apache-2.0).

import (
	"strconv"
	"time"
)

// Progress lines are spoken at most once every ProgressInterval, for a task
// that has run at least that long, after its progress has been quiet for
// ProgressQuiet (capped, so steady progress cannot delay a line forever).
// While the call cannot speak, a due line tries again after ProgressRetry.
const (
	ProgressInterval = time.Minute
	ProgressQuiet    = 800 * time.Millisecond
	ProgressRetry    = time.Second
)

// progressTask is the newest progress of one task that waits to be told.
type progressTask struct {
	started, firstOffered, updated, notBefore time.Time
	text                                      string
}

// progress holds the call's progress lines.
type progress struct {
	tasks         map[string]*progressTask
	lastAnnounced time.Time
	stop          func()
	spoken        int // counts lines told, for their keys
}

// Progress offers text as the latest progress of the work task, started at
// started, for Voice to tell the owner in a while. A newer offer replaces
// one not yet told.
func (c *Call) Progress(task string, started time.Time, text string) {
	c.do(func() {
		if c.provider.Caps.NoClientResponses {
			return // the provider cannot be asked to say it
		}
		now := c.now()
		t := c.progress.tasks[task]
		if t == nil {
			t = &progressTask{started: started, firstOffered: now}
			c.progress.tasks[task] = t
		}
		t.updated, t.text = now, text
		c.scheduleProgress()
	})
}

// EndProgress drops task's progress: the work answered, so it is not told.
func (c *Call) EndProgress(task string) {
	c.do(func() {
		delete(c.progress.tasks, task)
		c.scheduleProgress()
	})
}

// due is when task's line may be told.
func (c *Call) due(t *progressTask) time.Time {
	cadence := latest(t.started.Add(ProgressInterval), c.progress.lastAnnounced.Add(ProgressInterval), t.firstOffered, t.notBefore)
	quiet := t.updated.Add(ProgressQuiet)
	if capped := cadence.Add(ProgressQuiet); quiet.After(capped) {
		quiet = capped
	}
	return latest(cadence, quiet)
}

// nextProgress is the task whose line is due first.
func (c *Call) nextProgress() (string, *progressTask) {
	var id string
	var next *progressTask
	for task, t := range c.progress.tasks {
		if next == nil || c.due(t).Before(c.due(next)) || c.due(t).Equal(c.due(next)) && t.updated.Before(next.updated) {
			id, next = task, t
		}
	}
	return id, next
}

func (c *Call) scheduleProgress() {
	if c.progress.stop != nil {
		c.progress.stop()
		c.progress.stop = nil
	}
	_, next := c.nextProgress()
	if next == nil {
		return
	}
	c.progress.stop = c.later(max(0, c.due(next).Sub(c.now())), c.tellProgress)
}

// tellProgress tells the due line, or tries again shortly while the call
// cannot speak.
func (c *Call) tellProgress() {
	c.progress.stop = nil
	task, t := c.nextProgress()
	if t == nil {
		return
	}
	now := c.now()
	if c.due(t).After(now) {
		c.scheduleProgress()
		return
	}
	if !c.ready() || c.queue.waiting() {
		t.notBefore = now.Add(ProgressRetry)
		c.scheduleProgress()
		return
	}
	c.progress.spoken++
	c.progress.lastAnnounced = now
	delete(c.progress.tasks, task)
	c.queue.add("progress/"+task+"/"+strconv.Itoa(c.progress.spoken), "progress/"+task, progressPrompt+t.text)
	c.scheduleProgress()
}

// progressPrompt asks Voice for one short line about work still running.
const progressPrompt = "Work for the owner is still running. Tell the owner in a few words how it is going, in the first person, and do not say that it is done: "

func latest(times ...time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if t.After(out) {
			out = t
		}
	}
	return out
}
