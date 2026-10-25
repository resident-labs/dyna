package harness

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
)

// GitHub refusing requests must cost the agent one line, not the turn.
func TestTurnStartsWithOneLineWhenGitHubIsLimited(t *testing.T) {

	original := prepareRoomGitHub
	t.Cleanup(func() { prepareRoomGitHub = original })
	prepareRoomGitHub = func(context.Context, profile.Profile) (map[string]string, string, error) {
		return map[string]string{"GH_TOKEN": ""}, "GitHub is rate limited until 2026-10-23T15:00:00Z, so GitHub commands may fail this turn; continue without them.", nil
	}
	p := fakeworker.InstallClaude(t, `printf '%s' "$DYNA_FAKE_PROMPT"`)
	p.Name = "notice"
	p.Env = mergedEnv(p.Env, map[string]string{mode.EnvRoomID: "any-room"})
	result, err := RunSessionTurn(context.Background(), p, "repair the parser", t.TempDir(), true,
		JournalOptions{}, SteeringOptions{}, SessionOptions{})
	if err != nil {
		t.Fatalf("a limited GitHub failed the turn: %v", err)
	}
	if !strings.Contains(result.Output, "GitHub is rate limited until 2026-10-23T15:00:00Z") {
		t.Fatalf("turn prompt has no GitHub limit line: %q", result.Output)
	}
	if !strings.Contains(result.Output, "repair the parser") {
		t.Fatalf("turn prompt lost its task: %q", result.Output)
	}
}
