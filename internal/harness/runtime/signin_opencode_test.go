package runtime

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/account/native"
	harnesshome "dyna-agent/internal/harness/home"
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
			if !reflect.DeepEqual(st, before) {
				t.Fatalf("%q changed state: %+v", line, st)
			}
		}
	}
}

func TestOpenCodeGoSignInImportsKeyWithoutExposingIt(t *testing.T) {
	scope := t.TempDir()
	home := harnesshome.Home(scope)
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "opencode")
	// Echo the import on both streams: neither may reach the card or an error.
	script := `#!/bin/sh
[ "$#" -eq 2 ] && [ "$1" = auth ] && [ "$2" = import ] || exit 3
cat > "$HOME/import.json"
cat "$HOME/import.json"
cat "$HOME/import.json" >&2
exit "${SIGNIN_TEST_EXIT:-0}"
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"OPENCODE_CONFIG_DIR": scope, "OPENCODE_DB": filepath.Join(scope, "opencode.db")}
	host := native.Host{Executable: exe, Home: home, OpenCodeService: true, Env: func(key string) string { return env[key] }}
	key := "fixture-secret-" + strings.Repeat("k", 5000)
	var out bytes.Buffer
	if err := runOpenCodeGoSignIn(t.Context(), host, strings.NewReader(key+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var imported []struct {
		Integration string                     `json:"integrationID"`
		Value       struct{ Type, Key string } `json:"value"`
	}
	raw, err := os.ReadFile(filepath.Join(home, "import.json"))
	if err != nil || json.Unmarshal(raw, &imported) != nil || len(imported) != 1 || imported[0].Integration != "opencode-go" || imported[0].Value.Type != "key" || imported[0].Value.Key != key {
		t.Fatal("the private provider-key importer did not receive the key")
	}
	st := SignInState{Phase: signInStarting, AcceptsPaste: openCodeGoSignInSpec.AcceptsPaste}
	for _, line := range strings.Split(out.String(), "\n") {
		openCodeGoSignInSpec.Parse(&st, line)
	}
	if out.String() != openCodeGoKeyPrompt+"\n" || st.Phase != signInWaiting || !st.SecretInput || !st.AcceptsPaste {
		t.Fatal("key entry must show only a secret-input prompt")
	}
	t.Setenv("SIGNIN_TEST_EXIT", "1")
	out.Reset()
	err = runOpenCodeGoSignIn(t.Context(), host, strings.NewReader(key+"\n"), &out)
	if err == nil || strings.Contains(err.Error(), key) || out.String() != openCodeGoKeyPrompt+"\n" {
		t.Fatal("a failed key import must not expose its input or native output")
	}
}
