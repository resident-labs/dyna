package runtime

import (
	"reflect"
	"strings"
	"testing"
)

func TestMuseSignInParserReadsDevicePrompt(t *testing.T) {
	spec, ok := signInSpecs["muse"]
	if !ok {
		t.Fatal("muse has no in-app sign-in spec")
	}
	argv := []string{"/bin/muse", "login"}
	if got := spec.Argv(argv); !reflect.DeepEqual(got, argv) {
		t.Fatalf("argv = %q, want %q", got, argv)
	}
	st := SignInState{Phase: signInStarting}
	for _, line := range strings.Split("Open this page to sign in:\n  \x1b[32mhttps://auth.meta.com/oauth/device/?code=ABCD-EF12\x1b[0m\nconfirm this code matches:\n  ABCD-EF12\n\nWaiting for approval…\n", "\n") {
		spec.Parse(&st, plainOutput(line))
	}
	if st.URL != "https://auth.meta.com/oauth/device/?code=ABCD-EF12" || st.Code != "ABCD-EF12" || st.Phase != signInWaiting {
		t.Fatalf("state = %+v, want the provider device URL, code, and waiting phase", st)
	}
	for _, line := range []string{
		"https://example.com/oauth/device/?code=WXYZ-1234",
		"https://auth.meta.com.evil.test/oauth/device/?code=WXYZ-1234",
		"https://auth.meta.com/oauth/device/?code=invalid",
		"https://auth.meta.com/oauth/device/?code=WXYZ-1234&redirect=https://evil.test",
	} {
		before := st
		spec.Parse(&st, line)
		if !reflect.DeepEqual(st, before) {
			t.Fatalf("%q changed state: %+v", line, st)
		}
	}
}
