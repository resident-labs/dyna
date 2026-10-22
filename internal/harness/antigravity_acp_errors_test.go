package harness

import (
	"strings"
	"testing"
)

func TestAntigravityACPErrorMarkersRequireTheExactSession(t *testing.T) {
	session := "session-1"
	for _, marker := range []string{
		"Antigravity SDK error processing prompt for session " + session,
		"Agent connection lost for session " + session + " after rebuild retry",
		"Failed to rebuild agent for session " + session + "; connection unrecoverable",
	} {
		for _, prefix := range []string{"", "ERROR:root:", "E1021 22:00:00.123456 12345 server.py:5240] "} {
			diagnostic := prefix + marker + "\nResourceExhausted: quota exhausted"
			err := antigravityACPPromptError(session, diagnostic)
			if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
				t.Fatalf("lost backend failure: %v", err)
			}
			if err := antigravityACPPromptError("session-10", diagnostic); err != nil {
				t.Fatalf("another session's error was attributed: %v", err)
			}
		}
	}
}

func TestAntigravityACPDoesNotClassifyUnrelatedTextAsFailure(t *testing.T) {
	for _, text := range []string{
		"The assistant discussed a quota exhausted error.",
		"Antigravity SDK error processing prompt for session session-10",
		"example: Antigravity SDK error processing prompt for session session-1",
		"INFO:root:Antigravity SDK error processing prompt for session session-1",
		"Failed to rebuild agent for session session-1; connection unrecoverable (example)",
	} {
		if err := antigravityACPPromptError("session-1", text); err != nil {
			t.Fatalf("unrelated text became an error: %v", err)
		}
	}
}

func TestAntigravityACPFailureDiagnosticsStayBounded(t *testing.T) {
	text := "Antigravity SDK error processing prompt for session session-1\n" + strings.Repeat("x", 10000)
	err := antigravityACPPromptError("session-1", text)
	if err == nil || len(err.Error()) > 2100 {
		t.Fatalf("unbounded diagnostic: %v", err)
	}
}
