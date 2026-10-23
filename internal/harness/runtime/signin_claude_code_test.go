package runtime

import (
	"reflect"
	"testing"
)

func TestClaudeCodeSignInReadsAuthorizationLink(t *testing.T) {
	spec, ok := signInSpecs["claude-code"]
	if !ok || spec.Parse == nil || !spec.AcceptsPaste {
		t.Fatal("Claude Code needs an in-app sign-in with a paste box")
	}
	argv := []string{"/runtime/claude", "auth", "login", "--claudeai"}
	if spec.Argv != nil && !reflect.DeepEqual(spec.Argv(argv), argv) {
		t.Fatal("Claude Code must keep its native Claude account login command")
	}
	st := SignInState{Phase: signInStarting}
	for _, line := range []string{
		"Opening browser to sign in…",
		"If the browser didn't open, visit: https://example.com/cai/oauth/authorize?code=true",
		"https://claude.com:8443/cai/oauth/authorize?code=true",
		"https://claude.com/other?code=true",
		"https://user@claude.com/cai/oauth/authorize?code=true",
	} {
		spec.Parse(&st, line)
		if st.URL != "" || st.Phase != signInStarting {
			t.Fatalf("unrelated output changed the sign-in: %+v", st)
		}
	}
	link := "https://claude.com/cai/oauth/authorize?code=true&redirect_uri=https%3A%2F%2Fplatform.claude.com%2Foauth%2Fcode%2Fcallback&state=test"
	spec.Parse(&st, "If the browser didn't open, visit: "+link)
	if st.URL != link || st.Phase != signInWaiting || st.Message == "" {
		t.Fatalf("sign-in state = %+v, want the link and paste instructions", st)
	}
}
