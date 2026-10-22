package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"dyna-agent/internal/profile"
)

// The ACP server has no compact slash command. A configured manual
// threshold must fail before an ordinary model prompt can receive /compact.
func TestAntigravityACPDoesNotTreatCompactAsModelInput(t *testing.T) {
	_, err := buildInvocation(profile.Profile{
		Harness: profile.HarnessAntigravity, Compact: &profile.Compact{Tokens: 1},
	}, "task")
	if err == nil || !strings.Contains(err.Error(), "not supported by Antigravity ACP 1.3.0") {
		t.Fatalf("manual compaction was not refused: %v", err)
	}
}

// The service owns usage and compaction state; a client can miss usage events.
func TestOpenCodeCompactReadsStoredContextAndWaitsBeforeResume(t *testing.T) {
	log, steering := installOpenCodeCompactCLI(t)
	p := profile.Profile{Name: "oc-fake", Harness: profile.HarnessOpenCode, Compact: &profile.Compact{Tokens: 120, Windows: map[string]int64{"": 200000}}, Env: map[string]string{"DYNA_FAKE_LOG": log}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := RunSessionTurn(ctx, p, "first", t.TempDir(), false, JournalOptions{}, steering, SessionOptions{})
	if err != nil || first.SessionID == "" {
		t.Fatalf("first=%+v, %v", first, err)
	}
	next, err := RunSessionTurn(ctx, p, "next", t.TempDir(), false, JournalOptions{}, steering, SessionOptions{ResumeID: first.SessionID})
	if err != nil || next.CompactError != "" {
		t.Fatalf("next=%+v, %v", next, err)
	}
	// The conversation shows Dyna's own compaction with trigger dyna and the
	// context size that made it due.
	if c := next.Compaction; c == nil || c.State != "completed" || c.Before != 140 {
		t.Fatalf("compaction = %+v, want completed at 140 tokens", c)
	}
	calls := strings.Split(strings.TrimSpace(readTestFile(t, log)), "\n")
	want := []string{"run --format", "api session.context", "api experimental.session.wait", "api session.compact", "api experimental.session.wait", "run --format"}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls=%q, want %q", calls, want)
	}
}

func installOpenCodeCompactCLI(t *testing.T) (string, SteeringOptions) {
	t.Helper()
	// Guard subprocesses inherit the race detector's default one-second exit
	// delay. These short command fixtures need no grace period at exit.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	ready := filepath.Join(t.TempDir(), "start.fifo")
	if err := syscall.Mkfifo(ready, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OC_START_FIFO", ready)
	log := installFakeCLI(t, "opencode", `#!/bin/sh
set -eu
read -r started < "$OC_START_FIFO"
printf '%s %s\n' "$1" "$2" >> "$DYNA_FAKE_LOG"
if [ "$1" = api ]; then
 case "$2" in
 session.context) printf '%s\n' '{"data":[{"type":"assistant","tokens":{"input":100,"cache":{"read":30,"write":10}}},{"type":"idle"}]}' ;;
 session.compact) printf '%s\n' '{"data":{"type":"compaction"}}' ;;
 experimental.session.wait)
  case "${OC_WAIT_RESULT:-ok}" in
    missing) echo 'Wait endpoint is missing' >&2; exit 1 ;;
    changed) echo '{"unexpected":true}'; exit 0 ;;
    changed-after)
      if [ "$(grep -c '^api experimental.session.wait$' "$DYNA_FAKE_LOG")" -gt 1 ]; then
        echo '{"unexpected":true}'
      fi
      exit 0 ;;
  esac
  exit 0 ;;
 *) exit 1 ;;
 esac
else
 if [ -n "${OC_PROMPT_LOG:-}" ]; then
   for last; do :; done
   printf '%s\n' "$last" >> "$OC_PROMPT_LOG"
 fi
 printf '%s\n' '{"type":"text","sessionID":"ses_fixture","part":{"text":"done"}}'
fi
`)
	return log, SteeringOptions{Source: &testSource{}, StartGate: func(_ context.Context, _ string, launch func() error) error {
		if err := launch(); err != nil {
			return err
		}
		// Keep the fake command alive until guarded launch has completed.
		// Opening the FIFO waits for the child reader; no timer drives progress.
		return os.WriteFile(ready, []byte("started\n"), 0600)
	}}
}

func TestOpenCodeCompactSkipsMissingOrChangedWaitEndpoint(t *testing.T) {
	for _, mode := range []string{"missing", "changed", "changed-after"} {
		t.Run(mode, func(t *testing.T) {
			log, steering := installOpenCodeCompactCLI(t)
			p := profile.Profile{Name: "oc-fake", Harness: profile.HarnessOpenCode, Compact: &profile.Compact{Tokens: 120}, RuntimeInstructions: "RESTORED_RULE_CANARY", Env: map[string]string{"DYNA_FAKE_LOG": log, "OC_WAIT_RESULT": mode, "OPENCODE_CONFIG_CONTENT": "{}", "OC_PROMPT_LOG": log + ".prompt"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, err := RunSessionTurn(ctx, p, "continue safely", t.TempDir(), false, JournalOptions{}, steering, SessionOptions{ResumeID: "ses_fixture"})
			if err != nil {
				t.Fatalf("ordinary turn failed: %v", err)
			}
			if !strings.Contains(result.CompactError, "native automatic compaction stays enabled") || strings.Contains(result.CompactError, "\n") {
				t.Fatalf("want one clear compact warning, got %q", result.CompactError)
			}
			// The conversation records Dyna's failed compaction, with the context
			// size that made it due and the reason it failed.
			if c := result.Compaction; c == nil || c.State != "failed" || c.Before != 140 || c.Reason == "" {
				t.Fatalf("compaction = %+v, want failed at 140 tokens with a reason", c)
			}
			calls := readTestFile(t, log)
			admitted := mode == "changed-after"
			if strings.Contains(calls, "api session.compact") != admitted || !strings.Contains(calls, "run --format") {
				t.Fatalf("unsafe API sequence: %s", calls)
			}
			prompt := readTestFile(t, log+".prompt")
			if strings.Contains(prompt, "RESTORED_RULE_CANARY") != admitted {
				t.Fatalf("instruction restoration does not match possible admission: %q", prompt)
			}
		})
	}
}
