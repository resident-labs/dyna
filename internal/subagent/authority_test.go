package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
)

func TestWorkerAuthorityRefusesBeforeBackend(t *testing.T) {
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, "")
	ctx := context.Background()
	// A nil manager makes any backend access fail immediately.
	var m *Manager
	tests := []struct {
		action string
		call   func() error
	}{
		{"subagent start", func() error { _, _, err := m.Start(context.Background(), StartOptions{}); return err }},
		{"subagent start", func() error { _, err := m.Wake(ctx, "missing", WakeOptions{}); return err }},
		{"subagent stop", func() error { _, err := m.Stop(ctx, "missing"); return err }},
		{"subagent close", func() error { _, err := m.Close(ctx, "missing"); return err }},
		{"subagent reopen", func() error { _, err := m.Reopen(context.Background(), "missing"); return err }},
		{"subagent promote", func() error { _, err := m.Promote(context.Background(), "missing"); return err }},
		{"subagent rename", func() error { _, err := m.Rename(context.Background(), "missing", "renamed"); return err }},
		{"subagent adopt", func() error { _, err := m.Adopt(context.Background(), "missing", "parent"); return err }},
		{"subagent transfer", func() error {
			_, err := m.TransferContext(context.Background(), "missing", TransferOptions{})
			return err
		}},
		{"subagent transfer handoff", func() error { return m.SubmitHandoff(context.Background(), "missing", "", "", "", "") }},
		{"subagent transfer cancel", func() error { _, err := m.CancelTransfer(context.Background(), "missing"); return err }},
		{"subagent consolidate", func() error { _, err := m.Consolidate(context.Background(), ConsolidationOptions{}); return err }},
		{"subagent consolidate resume", func() error { _, err := m.ResumeConsolidation(context.Background(), "missing", ""); return err }},
		{"subagent consolidate cancel", func() error { _, err := m.CancelConsolidation(context.Background(), "missing", ""); return err }},
		{"room pause", func() error { _, err := m.PauseRoomSessions(ctx, "missing"); return err }},
		{"room resume", func() error { _, err := m.ResumeRoomSessions(ctx, "missing"); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			instead := "use subagent mode to control sessions"
			if tt.action == "subagent start" || tt.action == "subagent reopen" {
				instead = "use subagent mode to start work"
			}
			if tt.action == "room pause" || tt.action == "room resume" || tt.action == "subagent transfer handoff" || tt.action == "subagent transfer cancel" || tt.action == "subagent consolidate resume" || tt.action == "subagent consolidate cancel" {
				instead = "use subagent mode to control work"
			}
			want := tt.action + " is owner or root or session only; " + instead
			switch tt.action {
			case "subagent close":
				want = "subagent close is owner or root or another session only; a session becomes dormant when its turn ends, so it does not close itself"
			case "subagent adopt", "subagent promote":
				want = tt.action + " is owner or root or parent only; ask the session parent or root to move it"
			case "subagent rename":
				want = "subagent rename is owner or root or self or parent only; ask the session or its parent to rename it"
			}
			if err := tt.call(); err == nil || err.Error() != want {
				t.Fatalf("got %v; want %q", err, want)
			}
		})
	}
}

// Inside the engine, the session checks read the caller from ctx: a call
// with no actor on ctx is refused, never given the engine's own environment,
// which is the owner's (requirements item 3).
func TestEngineSessionChecksNeedACaller(t *testing.T) {
	t.Setenv(mode.EnvMode, "")
	t.Setenv(mode.EnvSession, "")
	t.Cleanup(authority.InEngine())
	ctx := context.Background()
	// A nil manager makes any backend access fail, so only the check can
	// return the refusal.
	var m *Manager
	for name, call := range map[string]func() error{
		"stop":            func() error { _, err := m.Stop(ctx, "missing"); return err },
		"close":           func() error { _, err := m.Close(ctx, "missing"); return err },
		"reopen":          func() error { _, err := m.Reopen(ctx, "missing"); return err },
		"promote":         func() error { _, err := m.Promote(ctx, "missing"); return err },
		"rename":          func() error { _, err := m.Rename(ctx, "missing", "renamed"); return err },
		"adopt":           func() error { _, err := m.Adopt(ctx, "missing", "parent"); return err },
		"dismiss-pending": func() error { return m.DismissPendingPost(ctx, "missing", "msg_a", true) },
		"clear-dormant":   func() error { _, err := m.ClearDormantSessions(ctx, "missing"); return err },
		"dismiss":         func() error { _, err := m.DismissSession(ctx, "missing"); return err },
		"pause":           func() error { _, err := m.PauseRoomSessions(ctx, "missing"); return err },
		"resume":          func() error { _, err := m.ResumeRoomSessions(ctx, "missing"); return err },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("passed the check and reached the backend: %v", r)
				}
			}()
			if err := call(); err == nil || !strings.Contains(err.Error(), "needs a caller") {
				t.Fatalf("err = %v; want a refusal for a call with no caller", err)
			}
		})
	}
}
