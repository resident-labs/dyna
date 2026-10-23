package runtime

import (
	"context"
	"encoding/json"
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
	selectStep := map[string]any{
		"message":      "Choose an OAuth provider.",
		"choices":      []map[string]string{{"id": "anthropic", "label": "Anthropic"}, {"id": "openai-codex", "label": "OpenAI Codex"}},
		"acceptsPaste": true,
	}
	raw, _ := json.Marshal(selectStep)
	spec.Parse(&st, "PI_SIGNIN "+string(raw))
	if st.Phase != signInWaiting || len(st.Choices) != 2 || st.Choices[0].ID != "anthropic" {
		t.Fatalf("provider selection = %+v", st)
	}
	raw, _ = json.Marshal(map[string]any{
		"url":          "https://claude.ai/oauth/authorize?state=test",
		"message":      "Paste the code Anthropic shows after you sign in.",
		"acceptsPaste": true,
	})
	spec.Parse(&st, "PI_SIGNIN "+string(raw))
	if st.URL != "https://claude.ai/oauth/authorize?state=test" || len(st.Choices) != 0 || !st.AcceptsPaste {
		t.Fatalf("authorization step = %+v", st)
	}
	for _, line := range []string{
		"PI_SIGNIN {broken",
		"PI_SIGNIN " + `{"url":"https://user@example.com/","acceptsPaste":true}`,
		"PI_SIGNIN " + `{"url":"http://example.com/","acceptsPaste":true}`,
	} {
		spec.Parse(&st, line)
		if st.URL != "https://claude.ai/oauth/authorize?state=test" {
			t.Fatalf("invalid event changed the link: %+v", st)
		}
	}
	raw, _ = json.Marshal(map[string]any{
		"url": "https://github.com/login/device", "code": "ABCD-EFGH",
		"message": "Open the link and enter this code.", "acceptsPaste": false,
	})
	spec.Parse(&st, "PI_SIGNIN "+string(raw))
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
