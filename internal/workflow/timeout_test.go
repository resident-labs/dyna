package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
)

func TestClampAgentTimeoutHasThirtyMinuteMinimum(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "short call", in: 10 * time.Minute, want: 30 * time.Minute},
		{name: "minimum", in: 30 * time.Minute, want: 30 * time.Minute},
		{name: "long call", in: 2 * time.Hour, want: 2 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampAgentTimeout(tc.in); got != tc.want {
				t.Fatalf("clampAgentTimeout(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveAgentTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		timeout    time.Duration
		timeoutSet bool
		profile    profile.Profile
		want       time.Duration
	}{
		{name: "default", timeout: defaultAgentTimeout, want: 5 * time.Hour},
		{name: "short explicit timeout", timeout: 10 * time.Minute, timeoutSet: true, want: 30 * time.Minute},
		{
			name:       "long explicit timeout",
			timeout:    6 * time.Hour,
			timeoutSet: true,
			profile:    profile.Profile{TimeoutSec: int((2 * time.Hour) / time.Second)},
			want:       6 * time.Hour,
		},
		{
			name:    "profile timeout without script timeout",
			timeout: defaultAgentTimeout,
			profile: profile.Profile{TimeoutSec: int((2 * time.Hour) / time.Second)},
			want:    2 * time.Hour,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAgentTimeout(tc.timeout, tc.timeoutSet, tc.profile); got != tc.want {
				t.Fatalf("resolveAgentTimeout(%s, %t, %#v) = %s, want %s", tc.timeout, tc.timeoutSet, tc.profile, got, tc.want)
			}
		})
	}
}

func TestExplicitShortAgentTimeoutIsClamped(t *testing.T) {
	worker := fakeworker.InstallClaude(t, "sleep 0.1; printf finished")
	worker.Name, worker.Default = "slow", true
	store := &profile.Store{Profiles: []profile.Profile{worker}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := executeTest(ctx, Options{
		ScriptSrc: `return await agent("finish", {profile: "slow", timeout: 0.01});`,
		Store:     store,
		WorkDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v; a sub-30-minute call timeout must be clamped", err)
	}
	if result != `"finished"` {
		t.Fatalf("Execute() = %s, want %q", result, "finished")
	}
}

func TestExecuteReportsReturnSerializationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := executeTest(ctx, Options{
		ScriptSrc: `const value = {}; value.self = value; return value;`,
		Store:     &profile.Store{},
		WorkDir:   t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "workflow failed:") {
		t.Fatalf("Execute() error = %v, want serialization failure", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Execute() waited for context cancellation instead of reporting serialization failure: %v", err)
	}
}

// A script that never yields stops when its context ends. goja runs it on
// the event loop; without an interrupt, Execute never returns.
func TestExecuteInterruptsABusyScriptOnCancel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := executeTest(ctx, Options{ScriptSrc: `while (true) {}`, Store: &profile.Store{}, WorkDir: t.TempDir()})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Execute returned no error for an interrupted script")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Execute did not return after its context ended")
	}
}
