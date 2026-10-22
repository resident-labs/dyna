package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

// A process-guard error once carried the Claude result JSON, whose
// "fallback_credit" field looked like a provider failure.
func TestProcessErrorDoesNotLeakStructuredStdout(t *testing.T) {
	stdout := `{"type":"result","subtype":"success","is_error":false,"usage":{"fallback_credit":null},"result":"hit the usage limit (HTTP 429)"}`
	err := attemptError(context.Background(), profile.Profile{Name: "sonnet", Harness: profile.HarnessClaudeCode}, []string{"claude"}, attempt{
		runErr: errors.New("retire Claude process group: native group has ended"), stdout: stdout,
	})
	if err == nil || IsProviderFailure(err.Error()) || strings.Contains(err.Error(), "fallback_credit") {
		t.Fatalf("process error = %v", err)
	}
	// Plain text output has no parser, so its tail stays in the error.
	err = attemptError(context.Background(), profile.Profile{Name: "custom", Harness: profile.HarnessCustom}, []string{"tool"}, attempt{
		runErr: errors.New("exit status 1"), stdout: "rate limit exceeded",
	})
	if err == nil || !IsProviderFailure(err.Error()) {
		t.Fatalf("text error = %v", err)
	}
}

// A turn that fails before the model runs, such as a signed-out account, marks
// its profile as not ready. A canceled turn says nothing about the profile.
func TestRuntimePrepareFailureIsNotReady(t *testing.T) {
	prepare := PrepareRuntime
	t.Cleanup(func() { PrepareRuntime = prepare })
	PrepareRuntime = func(context.Context, profile.Profile, *runtime.Pin) (profile.Profile, runtime.Pin, func(), error) {
		return profile.Profile{}, runtime.Pin{}, nil, errors.New("the active private account is not signed in")
	}
	root := t.TempDir()
	p := profile.Profile{Name: "sol", Harness: profile.HarnessCodex}
	_, err := RunSessionTurn(context.Background(), p, "task", root, false, JournalOptions{}, SteeringOptions{}, SessionOptions{})
	if err == nil || !IsNotReady(err.Error()) {
		t.Fatalf("prepare error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = RunSessionTurn(ctx, p, "task", root, false, JournalOptions{}, SteeringOptions{}, SessionOptions{}); err == nil || IsNotReady(err.Error()) {
		t.Fatalf("canceled prepare = %v", err)
	}
}
