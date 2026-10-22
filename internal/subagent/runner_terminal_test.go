package subagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/engineclient"
)

func TestRunnerTerminalScopeKeepsItsFirstDeadline(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "embedded", true: "detached"}[detached], func(t *testing.T) {
			w := &runnerWrites{}
			if detached {
				w.out = engineclient.NewOutbox(engineclient.New(""), "runner")
				defer w.out.Close(context.Background())
			}
			first := w.terminalContext()
			defer w.terminalCancel()
			deadline, ok := first.Deadline()
			if !ok || time.Until(deadline) > outboxClose {
				t.Fatal("terminal scope has no ten-minute deadline")
			}
			if again := w.terminalContext(); again != first {
				t.Fatal("a later terminal step reset the scope")
			}
			if !w.resumeActive() || !errors.Is(first.Err(), context.Canceled) {
				t.Fatal("active continuation did not disarm its unexpired scope")
			}
			second := w.terminalContext()
			if second == first {
				t.Fatal("a new finalization reused the disarmed scope")
			}
			w.terminalCancel()
			if w.resumeActive() {
				t.Fatal("an expired terminal scope resumed active work")
			}
			if again := w.terminalContext(); again != second {
				t.Fatal("an expired terminal scope got a fresh deadline")
			}
		})
	}
}

func TestRunnerTerminalScopeCloseReleasesEmbeddedTimer(t *testing.T) {
	w := &runnerWrites{}
	ctx := w.terminalContext()
	w.close(context.Background())
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("Close left the embedded runner's terminal timer active")
	}
}
