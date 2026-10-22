package harness

import (
	"os"
	"strings"
	"time"

	"dyna-agent/internal/runstore"
)

type journalSupervisor struct {
	opts   JournalOptions
	offset int64
	state  *JournalState
}

func newJournalSupervisor(opts JournalOptions) *journalSupervisor {
	if opts.Path == "" {
		return nil
	}
	state := opts.State
	if state == nil {
		state = NewJournalState()
	}
	state.mu.Lock()
	if state.lastActivity.IsZero() {
		state.lastActivity = time.Now()
	}
	if !state.offsetReady || state.offsetPath != opts.Path {
		state.offset = 0
		if info, err := os.Stat(opts.Path); err == nil {
			state.offset = info.Size()
		}
		state.offsetPath = opts.Path
		state.offsetReady = true
	}
	offset := state.offset
	state.mu.Unlock()
	return &journalSupervisor{opts: opts, offset: offset, state: state}
}

func (j *journalSupervisor) pollInterval() time.Duration {
	if j == nil || j.opts.IdleAfter <= 0 {
		return 500 * time.Millisecond
	}
	d := j.opts.IdleAfter / 20
	if d < 10*time.Millisecond {
		d = 10 * time.Millisecond
	}
	if d > 500*time.Millisecond {
		d = 500 * time.Millisecond
	}
	return d
}

func (j *journalSupervisor) poll() {
	if j == nil {
		return
	}
	entries, next, err := runstore.ReadAgentJournalPathFrom(j.opts.Path, j.offset)
	if err != nil {
		return
	}
	j.offset = next
	j.state.mu.Lock()
	j.state.offset = next
	j.state.offsetPath = j.opts.Path
	j.state.offsetReady = true
	j.state.mu.Unlock()
	active := false
	for _, entry := range entries {
		if !agentAuthoredJournalEntry(entry) {
			continue
		}
		active = true
		if j.opts.OnEntry != nil {
			j.opts.OnEntry(entry)
		}
	}
	if active {
		j.state.mu.Lock()
		j.state.lastActivity = time.Now()
		j.state.reminded = false
		j.state.unavailableNote = false
		j.state.hasEntry = true
		delete(j.state.reported, JournalNudgeIdle)
		j.state.mu.Unlock()
	}
}

func agentAuthoredJournalEntry(entry runstore.AgentJournalEntry) bool {
	if entry.TS <= 0 || strings.TrimSpace(entry.Kind) == "" || strings.TrimSpace(entry.Message) == "" {
		return false
	}
	switch entry.Kind {
	case "start", "nudge", "complete", "error":
		return false
	}
	if entry.Source == "agent" {
		return true
	}
	if entry.Source != "" {
		return false
	}
	// TODO: legacy/shim remove later — accepts journal entries without source; remove when all retained journals identify their author
	return true // tolerate valid hand-written legacy entries
}

func (j *journalSupervisor) reminderDue() bool {
	if j == nil || j.opts.IdleAfter <= 0 {
		return false
	}
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	return !j.state.reminded && time.Since(j.state.lastActivity) >= j.opts.IdleAfter
}

func (j *journalSupervisor) noteExternalResume() {
	if j == nil {
		return
	}
	j.state.mu.Lock()
	j.state.lastActivity = time.Now()
	j.state.reminded = false
	j.state.unavailableNote = false
	j.state.mu.Unlock()
}

// noteStreamNudge records an idle reminder written to a persistent-input
// worker's stdin: the worker was not interrupted, so the idle clock restarts.
func (j *journalSupervisor) noteStreamNudge() {
	if j == nil {
		return
	}
	j.markNudged(true, JournalNudgeIdle)
	j.state.mu.Lock()
	j.state.lastActivity = time.Now()
	j.state.reminded = true
	j.state.unavailableNote = false
	j.state.mu.Unlock()
}

func (j *journalSupervisor) hasAgentEntry() bool {
	if j == nil {
		return false
	}
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	return j.state.hasEntry
}

func (j *journalSupervisor) claimCompletionReminder() bool {
	if j == nil {
		return false
	}
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	if j.state.completionTried {
		return false
	}
	j.state.completionTried = true
	return true
}

func (j *journalSupervisor) markNudged(delivered bool, reason string) {
	if j == nil {
		return
	}
	j.state.mu.Lock()
	bit := uint8(1)
	if !delivered {
		bit = 2
	}
	if j.state.reported == nil {
		j.state.reported = make(map[string]uint8)
	}
	if j.state.reported[reason]&bit != 0 {
		j.state.mu.Unlock()
		return
	}
	j.state.reported[reason] |= bit
	if reason == JournalNudgeIdle {
		j.state.lastActivity = time.Now()
		j.state.reminded = true
		j.state.unavailableNote = !delivered
	}
	j.state.mu.Unlock()
	if j.opts.OnNudge != nil {
		j.opts.OnNudge(JournalNudge{Delivered: delivered, Reason: reason})
	}
}
