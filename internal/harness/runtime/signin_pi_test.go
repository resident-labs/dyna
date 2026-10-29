package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiSignInReadsNativeOAuthSteps(t *testing.T) {
	spec, ok := signInSpecs["pi"]
	if !ok || spec.Parse == nil || !spec.AcceptsPaste {
		t.Fatal("Pi needs an OAuth card with pasted input")
	}
	st := SignInState{Phase: signInStarting}
	spec.Parse(&st, "Pi startup diagnostics")
	if st.Phase != signInStarting {
		t.Fatalf("unrelated output changed the sign-in: %+v", st)
	}
	spec.Parse(&st, `{"type":"select","message":"Choose an OAuth provider.","options":[{"id":"anthropic","label":"Anthropic"},{"id":"openai-codex","label":"OpenAI Codex"}]}`)
	if st.Phase != signInWaiting || len(st.Choices) != 2 || st.Choices[0].ID != "anthropic" || !st.AcceptsPaste {
		t.Fatalf("provider selection = %+v", st)
	}
	spec.Parse(&st, `{"type":"auth_url","url":"https://claude.ai/oauth/authorize?state=test","instructions":"Paste the code Anthropic shows after you sign in."}`)
	if st.URL != "https://claude.ai/oauth/authorize?state=test" || len(st.Choices) != 0 || !st.AcceptsPaste {
		t.Fatalf("authorization step = %+v", st)
	}
	for _, line := range []string{
		`{"type":"auth_url","url":"https://user@example.com/"}`,
		`{"type":"auth_url","url":"http://example.com/"}`,
		"{broken",
	} {
		spec.Parse(&st, line)
		if st.URL != "https://claude.ai/oauth/authorize?state=test" {
			t.Fatalf("invalid event changed the link: %+v", st)
		}
	}
	spec.Parse(&st, `{"type":"device_code","verificationUri":"https://github.com/login/device","userCode":"ABCD-EFGH"}`)
	if st.URL != "https://github.com/login/device" || st.Code != "ABCD-EFGH" || st.AcceptsPaste {
		t.Fatalf("device-code step = %+v", st)
	}
}

func TestPiSignInHelperRejectsAnotherHome(t *testing.T) {
	scope := filepath.Join(t.TempDir(), ".runtime")
	t.Setenv("PI_CODING_AGENT_DIR", scope)
	t.Setenv("HOME", t.TempDir())
	err := PiSignInCommand(context.Background(), []string{"harness", "accounts", "_pi-signin", "--pi", "/fake/pi"})
	if err == nil || !strings.Contains(err.Error(), "private account home") {
		t.Fatalf("a different HOME reached Pi login: %v", err)
	}
}
