package harness

import (
	"context"
	"os"
	"sync"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

// claudeUsageCapture turns Claude's in-stream rate_limit_event into a usage
// observation on the admitted account, the way Muse quota errors are
// recorded. Observe runs on every live stdout line and keeps the windows of
// the newest event together with that event's own receipt time, so no later
// line can restamp older data. Finish runs after the process stops and sends
// that pair, after rechecking that the account is still the one the attempt
// was admitted on.
type claudeUsageCapture struct {
	manager   native.Manager
	before    native.Captured
	execution callspec.ExecutionRef
	outbox    *engineclient.Outbox
	now       func() time.Time

	mu       sync.Mutex
	windows  []workeroutput.ClaudeRateLimitWindow
	received time.Time
}

// captureClaudeUsage prepares the observation for one admitted attempt. It
// returns nil when the run cannot produce one; both methods are nil-safe.
func captureClaudeUsage(ctx context.Context, p profile.Profile, execution callspec.ExecutionRef, outbox *engineclient.Outbox) *claudeUsageCapture {
	if p.Harness != profile.HarnessClaudeCode || outbox == nil || execution.AdmissionID == "" || execution.Validate() != nil {
		return nil
	}
	if p.EffectiveOutput().Dialect != workeroutput.DialectClaudeStreamJSON {
		return nil
	}
	host := native.Host{Env: func(key string) string {
		if value, ok := p.Env[key]; ok {
			return value
		}
		return os.Getenv(key)
	}}
	if home := host.Getenv("HOME"); home != "" {
		host.Home = home
	}
	manager, ok := native.For("claude-code", host)
	if !ok {
		return nil
	}
	before, err := manager.Capture(ctx, "")
	if err != nil || len(before) != 1 {
		return nil
	}
	return &claudeUsageCapture{manager: manager, before: before[0], execution: execution, outbox: outbox, now: time.Now}
}

// Observe keeps the windows and receipt time of the newest line that itself
// carries usable windows. A later event without windows, or assistant output
// that merely mentions the event name, cannot restamp the saved reading: the
// observation must stay older than a probe that ran in between.
func (c *claudeUsageCapture) Observe(line string) {
	if c == nil {
		return
	}
	windows := workeroutput.ClaudeRateLimitWindows(line)
	if len(windows) == 0 {
		return
	}
	c.mu.Lock()
	c.windows, c.received = windows, c.now().UTC()
	c.mu.Unlock()
}

// Finish sends the saved windows with their own receipt time. Runs that never
// produced a live event send nothing.
func (c *claudeUsageCapture) Finish() {
	if c == nil {
		return
	}
	c.mu.Lock()
	windows, at := c.windows, c.received
	c.mu.Unlock()
	if len(windows) == 0 || at.IsZero() {
		return
	}
	// Cleanup still runs when the turn context was canceled.
	checkCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	after, err := c.manager.Capture(checkCtx, "")
	if err != nil || len(after) != 1 || c.before.Identity != after[0].Identity {
		return
	}
	observed := make([]callspec.ClaudeUsageWindow, len(windows))
	for i, w := range windows {
		observed[i] = callspec.ClaudeUsageWindow{ID: w.ID, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt}
	}
	c.outbox.Send("ObserveClaudeUsage", callspec.ClaudeUsageObservation{
		ExecutionRef: c.execution, Identity: c.before.Identity, ObservedAt: at, Windows: observed,
	})
}
