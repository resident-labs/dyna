package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/account/store"
)

func TestOpenCodeSignInParserReadsDevicePrompt(t *testing.T) {
	spec, ok := signInSpecs["opencode"]
	if !ok {
		t.Fatal("opencode has no in-app sign-in spec")
	}
	argv := []string{"/bin/opencode", "auth", "login"}
	want := []string{"/bin/opencode", "auth", "login", "opencode", "--method", "device"}
	if got := spec.Argv(argv); !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	t.Run("saved Go account keeps terminal", func(t *testing.T) {
		r := testPrivateRuntime(t)
		a, _, account := setupSharedLogin(t, "opencode")
		r.Accounts = a
		if err := a.transaction(func(st *store.State) error {
			entry := st.Find(account.ID)
			entry.Integration = "opencode-go"
			entry.Identity = "opencode:opencode-go:fixture"
			return nil
		}, true); err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(t.TempDir(), "opencode")
		if err := os.WriteFile(exe, []byte("#!/bin/sh\nread held\n"), 0700); err != nil {
			t.Fatal(err)
		}
		login, err := r.startPrivateLogin(account.ID, exe)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.CancelPrivateRuntimeLogin(account.ID) })
		if login.TerminalID == "" || login.SignIn != nil {
			t.Fatalf("login = %+v, want the native terminal for Go's key method", login)
		}
	})
	for _, prefix := range []string{"", "● "} {
		st := SignInState{Phase: signInStarting}
		for _, line := range strings.Split("│\n  "+prefix+"Enter code: ABCD-EF12\n  \x1b[32m"+prefix+"https://opencode.ai/console/device?user_code=ABCD-EF12&client_id=opencode-cli\x1b[0m\n", "\n") {
			spec.Parse(&st, plainOutput(line))
		}
		if st.URL != "https://opencode.ai/console/device?user_code=ABCD-EF12&client_id=opencode-cli" || st.Code != "ABCD-EF12" || st.Phase != signInWaiting {
			t.Fatalf("state = %+v, want the provider device URL, code, and waiting phase", st)
		}
		for _, line := range []string{
			"https://example.com/console/device?user_code=WXYZ-1234",
			"https://opencode.ai.evil.test/console/device?user_code=WXYZ-1234",
			"https://opencode.ai/console/device?user_code=invalid",
			"https://opencode.ai/console/device?user_code=WXYZ-1234&redirect=https://evil.test",
		} {
			before := st
			spec.Parse(&st, prefix+line)
			if st != before {
				t.Fatalf("%q changed state: %+v", line, st)
			}
		}
	}
}
