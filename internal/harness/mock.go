package harness

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// runMock is a deterministic in-process worker used for demos and tests.
// If the prompt contains a line starting with "RESPOND:", everything after
// that marker is echoed back verbatim (lets tests exercise schema parsing).
func runMock(ctx context.Context, prompt string) (string, error) {
	select {
	case <-time.After(mockWorkDelay(prompt)):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return mockResponse(prompt), nil
}

// MockDefaultDelay is how long a mock turn runs when its prompt has no SLEEP
// directive. Test packages whose turns need no hold set it to 0 in TestMain.
var MockDefaultDelay = 400 * time.Millisecond

func mockWorkDelay(prompt string) time.Duration {
	if match := mockHoldDirective.FindStringSubmatch(mockBody(prompt)); match != nil {
		if ms, err := strconv.Atoi(match[1]); err == nil {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return MockDefaultDelay
}

var mockUsageLimitDirective = regexp.MustCompile(`USAGE_LIMIT:([^\n]*)`)

// mockUsageLimit makes the mock turn end on a provider usage limit, like
// "USAGE_LIMIT: resets in 3h", until the turn moves to another account.
func mockUsageLimit(prompt string) (string, bool) {
	m := mockUsageLimitDirective.FindStringSubmatch(mockBody(prompt))
	if m == nil {
		return "", false
	}
	return "You've hit your session limit · " + strings.TrimSpace(m[1]), true
}

var mockHoldDirective = regexp.MustCompile(`SLEEP:(\d+)`)

var mockToolDirective = regexp.MustCompile("TOOL:`([^`]+)`")

// mockBody strips dyna's prompt envelopes down to the caller's task text.
func mockBody(prompt string) string {
	// Ignore the schema instruction suffix the engine may have appended.
	body := prompt
	// Worker metadata follows the task so identical tasks can share a prefix.
	// Strip only the final context block; a marker inside the task is task text.
	if strings.HasSuffix(body, "\n[/DYNA WORKER CONTEXT]") {
		if i := strings.LastIndex(body, "\n\n[DYNA WORKER CONTEXT]\n"); i >= 0 {
			body = body[:i]
		}
	}
	if i := strings.Index(body, "\n\n---\nOUTPUT FORMAT:"); i >= 0 {
		body = body[:i]
	}
	if i := strings.Index(body, "\n[ORIGINAL TASK]\n"); i >= 0 {
		body = body[i+len("\n[ORIGINAL TASK]\n"):]
	}
	// Subagent sessions: the first turn wraps the task in a charter, later turns
	// in a follow-up header.
	if i := strings.Index(body, "\n[TASK]\n"); i >= 0 && strings.Contains(body, "[DYNA SUBAGENT SESSION]") {
		body = body[i+len("\n[TASK]\n"):]
	}
	if strings.HasPrefix(body, "[DYNA FOLLOW-UP - turn ") {
		if i := strings.Index(body, "\n\n"); i >= 0 {
			body = body[i+2:]
		}
	}
	// Workflow workers always carry the no-delegation restriction suffix.
	if i := strings.Index(body, "\n\n[DYNA WORKFLOW RESTRICTION]"); i >= 0 {
		body = body[:i]
	}
	return body
}

func mockResponse(prompt string) string {
	body := mockBody(prompt)
	if i := strings.Index(body, "RESPOND:"); i >= 0 {
		return strings.TrimSpace(body[i+len("RESPOND:"):])
	}
	body = strings.TrimSpace(mockHoldDirective.ReplaceAllString(body, ""))
	head := body
	if len(head) > 120 {
		head = head[:120] + "…"
	}
	return "[mock worker] I received your task: " + head
}

// runMockSession is the mock harness as a resumable, steerable session: it
// has a session id, resumes by id, and consumes the session steering source. A
// "SLEEP:<ms>" directive in the prompt holds the turn open (default 400ms) so
// tests can steer a running turn deterministically.
func runMockSession(ctx context.Context, prompt string, journal JournalOptions, steeringOpts SteeringOptions, session SessionOptions, state *runState) (Result, error) {
	if steeringOpts.BeforeStart != nil {
		if err := steeringOpts.BeforeStart(ctx, prompt); err != nil {
			return Result{}, err
		}
	}
	if steeringOpts.OnStart != nil {
		steeringOpts.OnStart()
	}
	start := time.Now()
	id := session.ResumeID
	if id == "" {
		sid, err := newSessionID()
		if err != nil {
			return Result{}, err
		}
		id = "mock-" + sid
	}
	tracker := newSessionTracker(nil)
	tracker.preset(id)
	state.tracker = tracker
	if session.ResumeID == "" && session.OnSession != nil {
		session.OnSession(id)
	}
	if journal.Path != "" {
		e := runstore.AgentJournalEntry{
			TS: time.Now().UnixMilli(), Kind: "update", Message: "Mock worker oriented to the task.",
			Next: "Produce the deterministic mock response.", Source: "agent",
		}
		if err := runstore.AppendAgentJournalPath(journal.Path, e); err == nil && journal.OnEntry != nil {
			journal.OnEntry(e)
		}
	}
	source := steeringOpts.Source
	if source != nil {
		if err := source.Activate(); err != nil {
			return Result{}, err
		}
		defer source.Deactivate()
	}
	hold := mockWorkDelay(prompt)
	var offset int64
	var steered []string
	deliver := func(final bool) error {
		if source == nil {
			return nil
		}
		messages, next, err := source.Read(offset, final)
		if err != nil {
			return err
		}
		offset = next
		for _, m := range messages {
			if steeringOpts.OnDispatch != nil {
				if err := steeringOpts.OnDispatch(m); err != nil {
					return err
				}
			}
			if steeringOpts.OnMessage != nil {
				steeringOpts.OnMessage(m)
			}
			steered = append(steered, m.Message)
		}
		return nil
	}
	deadline := time.NewTimer(hold)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			return Result{Duration: time.Since(start)}, ctx.Err()
		case <-deadline.C:
			break wait
		case <-tick.C:
			if err := deliver(false); err != nil {
				return Result{Duration: time.Since(start)}, err
			}
			if len(steered) > 0 {
				break wait
			}
		}
	}
	// Final drain: anything accepted before the source closes becomes part of
	// this turn rather than being lost.
	if err := deliver(true); err != nil {
		return Result{Duration: time.Since(start)}, err
	}
	out := mockResponse(prompt)
	if len(steered) > 0 {
		out = "[mock worker] steered: " + strings.Join(steered, " | ")
		for _, m := range steered {
			if i := strings.Index(m, "RESPOND:"); i >= 0 {
				out = strings.TrimSpace(m[i+len("RESPOND:"):])
			}
		}
	}
	if journal.OutputPath != "" {
		if f, err := os.OpenFile(journal.OutputPath, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			_, _ = f.WriteString(out + "\n")
			f.Close()
		}
	}
	if journal.NormalizedOutputPath != "" {
		if f, err := os.OpenFile(journal.NormalizedOutputPath, os.O_APPEND|os.O_WRONLY, 0); err == nil {
			now := time.Now().UnixMilli()
			events := []workeroutput.Event{{V: workeroutput.SchemaVersion, Kind: "message", Role: "user", Text: mockBody(prompt), TS: start.UnixMilli()}}
			// TOOL:<command> directives become a command call and its
			// completion, so demos and desktop proofs show tool activity.
			for i, m := range mockToolDirective.FindAllStringSubmatch(mockBody(prompt), -1) {
				id, zero := "mock-"+strconv.Itoa(i), 0
				events = append(events,
					workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: "command", State: "started", ID: id, Input: m[1], TS: start.UnixMilli()},
					workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "tool", Name: "command", State: "completed", ID: id, Input: m[1], Output: "ok", Exit: &zero, TS: now})
			}
			events = append(events, workeroutput.Event{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", Text: out, TS: now})
			for _, event := range events {
				if b, err := json.Marshal(event); err == nil {
					_, _ = f.Write(append(b, '\n'))
				}
			}
			f.Close()
		}
	}
	// Mock token counts are a deterministic estimate (4 bytes per token).
	state.mu.Lock()
	state.usage.Add(workeroutput.Usage{InputTokens: int64(len(prompt)/4 + 1), OutputTokens: int64(len(out)/4 + 1)})
	state.mu.Unlock()
	return Result{Output: out, Duration: time.Since(start)}, nil
}
