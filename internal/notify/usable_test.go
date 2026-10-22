package notify

import (
	"testing"

	"dyna-agent/internal/subagent"
)

// A session whose first turn ended before the harness reported a session has
// no harness conversation to resume. A direct post still reaches it, and its
// next turn starts fresh. Recovery paths keep the strict rule, so a root or a
// parent fallback that cannot resume is still replaced or skipped.
func TestTakesPostAllowsIdleSessionWithoutHarnessSession(t *testing.T) {
	for _, status := range []string{subagent.StatusIdle, subagent.StatusDormant} {
		info := subagent.Info{Meta: subagent.Meta{Status: status, Resumable: true, Turns: 1}, Status: status}
		if why := takesPost(info); why != "" {
			t.Fatalf("%s session without a harness session cannot take a post: %s", status, why)
		}
		if why := usable(info); why == "" {
			t.Fatalf("%s session without a harness session is usable for a recovery path", status)
		}
	}
	// A session that never started a turn is still being created.
	unstarted := subagent.Info{Meta: subagent.Meta{Status: subagent.StatusIdle, Resumable: true}, Status: subagent.StatusIdle}
	if why := takesPost(unstarted); why == "" {
		t.Fatal("an unstarted session can still take a post")
	}
	closed := subagent.Info{Meta: subagent.Meta{Status: subagent.StatusClosed, Resumable: true, Turns: 1}, Status: subagent.StatusClosed}
	if why := takesPost(closed); why == "" {
		t.Fatal("a closed session can still take a post")
	}
	oneShot := subagent.Info{Meta: subagent.Meta{Status: subagent.StatusIdle, Turns: 1}, Status: subagent.StatusIdle}
	if why := takesPost(oneShot); why == "" {
		t.Fatal("a non-resumable session can still take a post")
	}
}
