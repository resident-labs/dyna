package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
)

// A process-guard error once carried the Claude result JSON, whose
// "fallback_credit" field looked like a provider failure.
func TestProcessErrorDoesNotLeakStructuredStdout(t *testing.T) {
	stdout := `{"type":"result","subtype":"success","is_error":false,"usage":{"fallback_credit":null},"result":"hit the usage limit (HTTP 429)"}`
	err := attemptError(context.Background(), profile.Profile{Name: "sonnet", Harness: profile.HarnessClaudeCode}, []string{"claude"}, attempt{
		runErr: errors.New("retire Claude process group: native group has ended"), stdout: stdout,
	})
	if err == nil || strings.Contains(err.Error(), "fallback_credit") {
		t.Fatalf("process error = %v", err)
	}
	if reason, ok := providerfailure.ReasonOf(err); ok {
		t.Fatalf("a process guard named provider reason %q", reason)
	}
	// Plain text output has no parser, so its tail stays in the message for the
	// owner. Model text never names a provider reason.
	err = attemptError(context.Background(), profile.Profile{Name: "custom", Harness: profile.HarnessCustom}, []string{"tool"}, attempt{
		runErr: errors.New("exit status 1"), stdout: "rate limit exceeded",
	})
	if err == nil || !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("text error = %v", err)
	}
	if reason, ok := providerfailure.ReasonOf(err); ok {
		t.Fatalf("model stdout named provider reason %q", reason)
	}
	// The process's own stderr is a trusted boundary.
	err = attemptError(context.Background(), profile.Profile{Name: "custom", Harness: profile.HarnessCustom}, []string{"tool"}, attempt{
		runErr: errors.New("exit status 1"), stderr: "Error: 429 rate limit exceeded",
	})
	if !providerfailure.Is(err, providerfailure.Quota) {
		t.Fatalf("stderr reason was lost: %v", err)
	}
}

// A provider failure reason comes only from a trusted boundary, and each reason
// allows exactly one recovery. Model output that talks about a limit or credit
// routes nothing, so it can never move an account, a model or a profile.
func TestProviderFailureReasonsRouteRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness string
		stdout  string
		stderr  string
		runErr  error
		want    providerfailure.Reason
	}{
		{
			name:    "model talks about a limit and credit",
			harness: profile.HarnessCustom,
			stdout:  "The provider returned HTTP 429 and my credit balance is low; retry the usage limit later.",
		},
		{
			name:    "structured stdout status is a completed turn",
			harness: profile.HarnessClaudeCode,
			stdout:  `{"type":"result","subtype":"success","is_error":false,"result":"HTTP 429 rate limit"}` + "\n",
			stderr:  "exit status 1",
		},
		{name: "stderr names a rate limit", harness: profile.HarnessCustom, stderr: "HTTP 429: rate limit exceeded", want: providerfailure.Quota},
		{name: "stderr names a busy provider", harness: profile.HarnessCustom, stderr: "provider overloaded, retry in a minute", want: providerfailure.Capacity},
		{name: "stderr names a rejected sign-in", harness: profile.HarnessCustom, stderr: "authentication_error: invalid api key", want: providerfailure.Auth},
		{name: "stderr names a server failure", harness: profile.HarnessCustom, stderr: "internal server error", want: providerfailure.Unavailable},
		// Stderr holds log lines as well as provider diagnostics, so a bare number
		// or a provider word alone is not evidence of a failure.
		{name: "stderr reads a line number", harness: profile.HarnessCustom, stderr: "reading line 429 of the session log"},
		{name: "stderr mentions quota settings", harness: profile.HarnessCustom, stderr: "checking quota configuration for the room"},
		{name: "stderr names no provider", harness: profile.HarnessCustom, stderr: "the model wondered about its credit balance"},
		{name: "stderr names a bare invalid key", harness: profile.HarnessCustom, stderr: "invalid key in configuration"},
		{
			name:    "codex protocol failure names the quota",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"message":"You've hit your session limit, resets in 3h"}}`,
			want:    providerfailure.Quota,
		},
		{
			// The message uses none of the provider vocabulary; its structured type
			// names the failure instead.
			name:    "codex structured type names the sign-in",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"type":"authentication_error","message":"bad token"}}`,
			want:    providerfailure.Auth,
		},
		{
			name:    "codex structured status names the quota",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"code":429,"message":"slow down"}}`,
			want:    providerfailure.Quota,
		},
		{
			name:    "codex numeric code names the sign-in",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"code":401,"message":"bad token"}}`,
			want:    providerfailure.Auth,
		},
		{
			name:    "codex numeric status names the busy provider",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"status":503,"message":"try later"}}`,
			want:    providerfailure.Capacity,
		},
		{
			// An opaque protocol failure still takes its reason from the process's
			// own stderr, so the profile can be blocked and no retry is spent.
			name:    "codex opaque failure reads stderr",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"message":"closed"}}`,
			stderr:  "authentication_error: invalid api key",
			want:    providerfailure.Auth,
		},
		{
			// The protocol record is opaque, but the harness already named the reason
			// of that same failure.
			name:    "opencode opaque record keeps the harness reason",
			harness: profile.HarnessOpenCode,
			stdout:  `{"type":"error","sessionID":"s","error":{"name":"APIError","data":{"message":"the provider said no"}}}`,
			runErr:  providerfailure.Tag(providerfailure.Auth, errors.New("OpenCode turn failed: the provider said no")),
			want:    providerfailure.Auth,
		},
		{
			name:    "codex protocol failure is unclear",
			harness: profile.HarnessCodex,
			stdout:  `{"type":"turn.failed","error":{"message":"stream closed unexpectedly"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := attempt{stdout: tc.stdout, stderr: tc.stderr, runErr: tc.runErr}
			if a.runErr == nil && tc.harness != profile.HarnessCodex {
				a.runErr = errors.New("exit status 1")
			}
			err := attemptError(context.Background(), profile.Profile{Name: "worker", Harness: tc.harness}, []string{"tool"}, a)
			if err == nil {
				t.Fatal("the attempt succeeded")
			}
			got, ok := providerfailure.ReasonOf(err)
			if ok != (tc.want != "") || got != tc.want {
				t.Fatalf("reason = %q, %v; want %q", got, ok, tc.want)
			}
			if !ok {
				return
			}
			// The one recovery of the reason, and the text that explains it, both
			// follow the typed reason rather than the message.
			if got.Action() == "" {
				t.Fatalf("reason %q has no action", got)
			}
			if !providerfailure.Is(err, tc.want) {
				t.Fatalf("Is(%q) is false for %v", tc.want, err)
			}
		})
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
