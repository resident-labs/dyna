package management

import "testing"

func TestDynaOAuthCompletionHook(t *testing.T) {
	oldEnabled, oldHook := DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook
	defer func() { DynaMemoryOAuthCallbacks, DynaOAuthCompletionHook = oldEnabled, oldHook }()
	DynaMemoryOAuthCallbacks = true
	var state, id string
	DynaOAuthCompletionHook = func(s, a string) { state, id = s, a }
	CompleteOAuthSessionWithOutcome("invented-state", "invented-id", "", true, false, false, 0, 0, 0, 0, "", "")
	if state != "invented-state" || id != "invented-id" {
		t.Fatal("saved completion was not forwarded")
	}
}
