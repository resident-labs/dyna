package runtime

import (
	"reflect"
	"strings"
	"testing"
)

func TestGrokSignInParserReadsDevicePrompt(t *testing.T) {
	spec, ok := signInSpecs["grok"]
	if !ok {
		t.Fatal("grok has no in-app sign-in spec")
	}
	argv := []string{"/bin/grok", "--no-leader", "login", "--leader-socket", "/account/leader.sock"}
	want := append(append([]string(nil), argv...), "--device-auth")
	if got := spec.Argv(argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	st := SignInState{Phase: signInStarting}
	for _, line := range strings.Split("To sign in, open this URL in your browser:\n\n  \x1b[32mhttps://accounts.x.ai/oauth2/device?user_code=ABCD-EF12\x1b[0m\n", "\n") {
		spec.Parse(&st, plainOutput(line))
	}
	if st.URL != "https://accounts.x.ai/oauth2/device?user_code=ABCD-EF12" || st.Code != "ABCD-EF12" || st.Phase != signInWaiting {
		t.Fatalf("state = %+v, want the provider device URL, code, and waiting phase", st)
	}
	for _, line := range []string{
		"https://example.com/oauth2/device?user_code=WXYZ-1234",
		"https://accounts.x.ai.evil.test/oauth2/device?user_code=WXYZ-1234",
		"https://accounts.x.ai/oauth2/device?user_code=invalid",
		"https://accounts.x.ai/oauth2/device?user_code=WXYZ-1234&redirect=https://evil.test",
	} {
		before := st
		spec.Parse(&st, line)
		if !reflect.DeepEqual(st, before) {
			t.Fatalf("%q changed state: %+v", line, st)
		}
	}
}
