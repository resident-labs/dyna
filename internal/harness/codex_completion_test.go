package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
)

func TestCodexEmptyCompletionRequiresTerminalAndPermission(t *testing.T) {
	for _, tc := range []struct {
		name, stream, want string
		allowed            bool
		runErr             error
	}{
		{name: "silent completion", stream: `{"type":"item.completed","item":{"type":"agent_message","text":""}}
{"type":"turn.completed"}`, allowed: true},
		{name: "tools only completion", stream: `{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0}}
{"type":"turn.completed"}`, allowed: true},
		{name: "required report", stream: `{"type":"turn.completed"}`, want: "completed Codex turn without required final response"},
		{name: "missing terminal", stream: `{"type":"item.completed","item":{"type":"agent_message","text":""}}`, allowed: true, want: "without turn.completed"},
		{name: "tools without terminal", stream: `{"type":"item.completed","item":{"type":"command_execution","status":"completed","exit_code":0}}`, allowed: true, want: "without turn.completed"},
		{name: "new turn without terminal", stream: `{"type":"turn.completed"}
{"type":"turn.started"}`, allowed: true, want: "without turn.completed"},
		{name: "provider failure", stream: `{"type":"turn.failed","error":{"message":"provider refused request"}}`, allowed: true, want: "provider refused request"},
		{name: "transport failure", stream: `{"type":"turn.completed"}`, allowed: true, runErr: errors.New("transport closed"), want: "transport closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := attemptError(context.Background(), profile.Profile{Name: "sol", Harness: profile.HarnessCodex}, []string{"codex"}, attempt{stdout: tc.stream, allowEmptyOutput: tc.allowed, runErr: tc.runErr})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCodexAppServerOutputKeepsOnlyFinalAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, stream, want string
	}{
		{name: "last final answer wins", stream: `{"type":"item.completed","item":{"type":"agent_message","text":"first final","phase":"final_answer"}}
{"type":"item.completed","item":{"type":"agent_message","text":"last final","phase":"final_answer"}}`, want: "last final"},
		{name: "commentary after final is ignored", stream: `{"type":"item.completed","item":{"type":"agent_message","text":"answer","phase":"final_answer"}}
{"type":"item.completed","item":{"type":"agent_message","text":"progress note","phase":"commentary"}}`, want: "answer"},
		{name: "commentary alone is not a final answer", stream: `{"type":"item.completed","item":{"type":"agent_message","text":"progress note","phase":"commentary"}}`},
		{name: "missing phase counts as final", stream: `{"type":"item.completed","item":{"type":"agent_message","text":"legacy"}}`, want: "legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexAppServerOutput(tc.stream); got != tc.want {
				t.Fatalf("codexAppServerOutput() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexSteeringCompletionAndRecoveryCeiling(t *testing.T) {
	old := steerHookSupport
	steerHookSupport = func(string, string) bool { return false }
	t.Cleanup(func() { steerHookSupport = old })
	for _, tc := range []struct {
		name                              string
		allowed, parentSteering, terminal bool
		textAt, steerAt, wantCalls        int
		wantNudged                        bool
		wantError                         string
	}{
		{name: "silent steering after final", allowed: true, terminal: true, textAt: 1, steerAt: 1, wantCalls: 2},
		{name: "parent steering requires final", allowed: true, parentSteering: true, terminal: true, textAt: 1, steerAt: 1, wantCalls: 3, wantNudged: true, wantError: "same-session nudge failed"},
		{name: "missing terminal retries once", allowed: true, wantCalls: 2, wantNudged: true, wantError: "without turn.completed"},
		{name: "steering after nudge cannot nudge again", terminal: true, textAt: 2, steerAt: 2, wantCalls: 3, wantNudged: true, wantError: "same-session nudge failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &testSource{}
			allowed, calls := tc.allowed, 0
			delay := time.Duration(0)
			result, err := RunWithOptions(context.Background(), profile.Profile{Name: "codex-fixture", Harness: profile.HarnessCodex}, "task", t.TempDir(), true, JournalOptions{}, SteeringOptions{
				Source: source,
				OnDispatch: func(runstore.SteeringMessage) error {
					if tc.parentSteering {
						allowed = false
					}
					return nil
				},
			}, SessionOptions{RecoveryDelay: &delay}, ExecutionOptions{
				AcceptEmptyOutput: func() bool { return allowed },
				ProcessRunner: func(_ context.Context, _ []string, stdout, _ io.Writer) error {
					calls++
					if calls > tc.wantCalls {
						return errors.New("unexpected extra recovery")
					}
					fmt.Fprintln(stdout, `{"type":"thread.started","thread_id":"completion-fixture"}`)
					fmt.Fprintln(stdout, `{"type":"turn.started"}`)
					text := ""
					if calls == tc.textAt {
						text = "saved final"
					}
					fmt.Fprintf(stdout, "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%q}}\n", text)
					if tc.terminal {
						fmt.Fprintln(stdout, `{"type":"turn.completed"}`)
					}
					// Deliver at the process boundary, after its final response was written.
					if calls == tc.steerAt {
						source.submit("new steering")
					}
					return nil
				},
			})
			if calls != tc.wantCalls || result.Nudged != tc.wantNudged {
				t.Fatalf("calls=%d result=%+v error=%v", calls, result, err)
			}
			if tc.wantError == "" {
				if err != nil || result.Output != "" {
					t.Fatalf("silent completion = %+v, %v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error=%v, want %q", err, tc.wantError)
			}
		})
	}
}

