package harness

import (
	"testing"

	"dyna-agent/internal/profile"
)

// Grok runs over ACP: its invocation carries no argv, only the prompt, and
// every resume stays on the session's long-lived agent process.
func TestGrokAcpInvocationCarriesNoArgv(t *testing.T) {
	inv, err := buildInvocation(profile.Profile{Name: "g", Harness: profile.HarnessGrok}, "task")
	if err != nil {
		t.Fatal(err)
	}
	if inv.initial.acpPrompt != "task" || len(inv.initial.argv) != 0 {
		t.Fatalf("initial=%+v", inv.initial)
	}
	resumed := inv.resume("session-id", "next task")
	if resumed.acpPrompt != "next task" || len(resumed.argv) != 0 {
		t.Fatalf("resume=%+v", resumed)
	}
	if !Resumable(profile.Profile{Harness: profile.HarnessGrok}) {
		t.Fatal("grok ACP must stay resumable")
	}
}
