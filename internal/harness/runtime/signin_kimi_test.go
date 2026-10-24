package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

func TestKimiCodeSignInParserReadsRegionAndDeviceSteps(t *testing.T) {
	spec, ok := signInSpecs["kimi-code"]
	if !ok || spec.Parse == nil || !spec.AcceptsPaste {
		t.Fatal("Kimi Code needs a region and device-code sign-in card")
	}
	st := SignInState{Phase: signInStarting}
	choice := SignInState{
		Message: "Choose a Kimi region.", AcceptsPaste: true,
		Choices: []SignInChoice{{ID: "mainland-cn", Label: "Kimi.com (mainland China)"}, {ID: "global", Label: "Kimi.ai (global)"}},
	}
	raw, _ := json.Marshal(choice)
	spec.Parse(&st, "KIMI_SIGNIN "+string(raw))
	if st.Phase != signInWaiting || len(st.Choices) != 2 || st.Choices[1].ID != "global" || !st.AcceptsPaste {
		t.Fatalf("region step = %+v", st)
	}
	spec.Parse(&st, "Opening browser for Kimi device login: https://auth.kimi.ai/verify?user_code=ABCD-EF12")
	spec.Parse(&st, "If the browser did not open, paste the URL above and enter code: ABCD-EF12")
	if st.URL != "https://auth.kimi.ai/verify?user_code=ABCD-EF12" || st.Code != "ABCD-EF12" || st.Phase != signInWaiting || st.AcceptsPaste {
		t.Fatalf("device step = %+v", st)
	}
	before := st
	spec.Parse(&st, "Opening browser for Kimi device login: https://auth.kimi.ai.evil.test/verify?user_code=WXYZ-1234")
	if st.URL != before.URL || st.Code != before.Code {
		t.Fatalf("untrusted URL changed the sign-in: %+v", st)
	}
}

func TestKimiCodeSignInHelperSelectsGlobalRegion(t *testing.T) {
	scope := filepath.Join(t.TempDir(), ".runtime")
	if err := os.MkdirAll(scope, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIMI_CODE_HOME", scope)
	t.Setenv("HOME", harnesshome.Home(scope))
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	executable := filepath.Join(dir, "kimi")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > \"$KIMI_TEST_ARGV\"\n" +
		"printf '%s\n' 'Opening browser for Kimi device login: https://auth.kimi.ai/verify?user_code=ABCD-EF12' >&2\n" +
		"printf '%s\n' 'If the browser did not open, paste the URL above and enter code: ABCD-EF12' >&2\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIMI_TEST_ARGV", argvPath)
	var out bytes.Buffer
	if err := runKimiOAuth(context.Background(), executable, scope, strings.NewReader("global\n"), &out); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil || string(argv) != "login --region global" {
		t.Fatalf("native argv = %q, %v", string(argv), err)
	}
	text := out.String()
	if !strings.Contains(text, "KIMI_SIGNIN ") || !strings.Contains(text, "https://auth.kimi.ai/verify?user_code=ABCD-EF12") || !strings.Contains(text, "ABCD-EF12") {
		t.Fatalf("sign-in output is missing region and device steps: %q", text)
	}
}
