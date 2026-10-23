package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const openCodeTestList = `[{"id":"opencode-go","connections":[{"type":"credential","id":"cred_old","label":"private-label"},{"type":"credential","id":"cred_new","label":"private-label"}]},{"id":"opencode","connections":[{"type":"credential","id":"cred_opencode"}]},{"id":"env-provider","connections":[{"type":"env","name":"FAKE_API_KEY"}]}]`

func openCodeTestManager(t *testing.T) *openCodeManager {
	t.Helper()
	home := t.TempDir()
	exe := filepath.Join(home, "fake-opencode")
	// Like OpenCode 2.0.32, fail when OPENCODE_TEST_HOME is set but empty, and
	// fail auth list without --standalone, as when another scope holds the
	// managed service port.
	script := `#!/bin/sh
if [ "${OPENCODE_TEST_HOME+set}" = set ] && [ -z "$OPENCODE_TEST_HOME" ]; then
 echo "Expected path to be a non-empty string" >&2; exit 1
elif [ "$1 $2" = "auth list" ] && [ "$5" != "--standalone" ]; then
 echo "Managed service port 49374 on 127.0.0.1 is already in use by another process." >&2; exit 1
elif [ "$1 $2" = "auth list" ]; then
 cat "$HOME/list.json"
elif [ "$1 $2" = "auth switch" ]; then
 printf '%s\n' "$@" > "$HOME/switch-args"
 printf '%s\n' "$OPENCODE_DB" "$XDG_STATE_HOME" "$OPENCODE_CONFIG_DIR" > "$HOME/switch-env"
 if [ -f "$HOME/fail-switch" ]; then echo never-show-this >&2; exit 1; fi
else
 echo never-show-this >&2; exit 1
fi
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "list.json"), []byte(openCodeTestList), 0600); err != nil {
		t.Fatal(err)
	}
	return &openCodeManager{host: Host{Home: home, Executable: exe, Env: func(string) string { return "" }}}
}
func TestOpenCodeCaptureAndListNative(t *testing.T) {
	m := openCodeTestManager(t)
	active, err := m.Capture(context.Background(), "")
	if err != nil || len(active) != 2 || active[0].Identity != "opencode:opencode-go:cred_old" {
		t.Fatal("active refs", err)
	}
	all, err := m.ListNative(context.Background())
	if err != nil || len(all) != 3 {
		t.Fatal("all refs", err)
	}
	for _, c := range all {
		payload := string(c.Credential.Payload)
		if strings.Contains(payload, "private-label") || strings.Contains(payload, "access") || strings.Contains(payload, "refresh") {
			t.Fatal("reference contains auth material")
		}
		if _, err := openCodeUnpack(c.Credential); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = m.Capture(context.Background(), t.TempDir()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted isolated root")
	}
}
func TestOpenCodeNativeSwitchAndPrevious(t *testing.T) {
	m := openCodeTestManager(t)
	all, err := m.ListNative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous, err := m.Activate(context.Background(), "opencode-go", all[1].Credential)
	if err != nil || len(previous) != 1 || previous[0].Identity != "opencode:opencode-go:cred_old" {
		t.Fatal("previous ref", err)
	}
	raw, err := os.ReadFile(filepath.Join(m.host.Home, "switch-args"))
	if err != nil || string(raw) != "auth\nswitch\nopencode-go\ncred_new\n" {
		t.Fatal("wrong native arguments", err)
	}
	scope, _ := m.scope()
	env, err := os.ReadFile(filepath.Join(m.host.Home, "switch-env"))
	if err != nil || !strings.Contains(string(env), scope.Database) {
		t.Fatal("wrong native scope", err)
	}
	// A failed native command must still return the current reference.
	if err = os.WriteFile(filepath.Join(m.host.Home, "fail-switch"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	previous, err = m.Activate(context.Background(), "opencode-go", all[1].Credential)
	if !errors.Is(err, ErrPartialActivation) || len(previous) != 1 || strings.Contains(err.Error(), "never-show-this") {
		t.Fatal("lost previous or raw error")
	}
}
func TestOpenCodeRefScopeAndSchemaGuards(t *testing.T) {
	m := openCodeTestManager(t)
	all, _ := m.ListNative(context.Background())
	c := all[1].Credential
	ref, _ := openCodeUnpack(c)
	ref.Scope.Database = filepath.Join(t.TempDir(), "other.db")
	c.Payload, _ = json.Marshal(map[string]openCodeRef{"ref": ref})
	if _, err := m.Activate(context.Background(), "opencode-go", c); !errors.Is(err, ErrSchema) {
		t.Fatal("accepted wrong scope")
	}
	if _, err := m.Activate(context.Background(), "opencode", all[1].Credential); !errors.Is(err, ErrSchema) {
		t.Fatal("accepted wrong integration")
	}
	c = all[1].Credential
	var roles map[string]any
	_ = json.Unmarshal(c.Payload, &roles)
	roles["ref"].(map[string]any)["refresh"] = "fixture-secret"
	c.Payload, _ = json.Marshal(roles)
	if _, err := openCodeUnpack(c); !errors.Is(err, ErrSchema) {
		t.Fatal("accepted grant in ref")
	}
	if _, err := os.Stat(filepath.Join(m.host.Home, "switch-args")); !os.IsNotExist(err) {
		t.Fatal("invalid target ran switch")
	}
}
func TestOpenCodeRejectsMalformedAndUnavailableTargets(t *testing.T) {
	for _, input := range []string{"null", "{}", `[{"id":"opencode-go","connections":[{"type":"credential","id":"bad id"}]}]`, `[{"id":"opencode-go"},{"id":"opencode-go"}]`} {
		m := openCodeTestManager(t)
		_ = os.WriteFile(filepath.Join(m.host.Home, "list.json"), []byte(input), 0600)
		if _, err := m.Capture(context.Background(), ""); !errors.Is(err, ErrSchema) {
			t.Fatal("accepted malformed list")
		}
	}
	m := openCodeTestManager(t)
	all, _ := m.ListNative(context.Background())
	_ = os.WriteFile(filepath.Join(m.host.Home, "list.json"), []byte(`[{"id":"opencode-go","connections":[{"type":"credential","id":"cred_old"},{"type":"credential","id":"cred_new","status":{"code":"needs_auth"}}]}]`), 0600)
	previous, err := m.Activate(context.Background(), "opencode-go", all[1].Credential)
	if !errors.Is(err, ErrInvalidGrant) || len(previous) != 1 {
		t.Fatal("accepted unusable credential", err)
	}
	_ = os.WriteFile(filepath.Join(m.host.Home, "list.json"), []byte(`[]`), 0600)
	if _, err = m.Capture(context.Background(), ""); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal(err)
	}
	if _, err = m.Activate(context.Background(), "opencode-go", all[1].Credential); !errors.Is(err, ErrNotSignedIn) {
		t.Fatal("accepted removed credential")
	}
}
func TestOpenCodeLoginAndRefresh(t *testing.T) {
	m := openCodeTestManager(t)
	argv, env, err := m.LoginCommand("", "opencode-go")
	if err != nil || !reflect.DeepEqual(argv, []string{m.host.Executable, "auth", "login", "opencode-go"}) || !strings.Contains(strings.Join(env, "\n"), "opencode-v2.db") {
		t.Fatal("in-place login", err)
	}
	if _, _, err = m.LoginCommand(t.TempDir(), "opencode-go"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted isolated import")
	}
	if _, err = os.Stat(filepath.Join(m.host.Home, ".local")); !os.IsNotExist(err) {
		t.Fatal("login command created files")
	}
	all, _ := m.ListNative(context.Background())
	unchanged, err := m.Refresh(context.Background(), all[0])
	if err != nil || !reflect.DeepEqual(unchanged, all[0]) || m.Caps().Refresh || m.Caps().IsolatedLogin {
		t.Fatal("reference refresh changed input")
	}
}
func TestOpenCodeUsesHostPaths(t *testing.T) {
	m := openCodeTestManager(t)
	custom := t.TempDir()
	values := map[string]string{"XDG_DATA_HOME": filepath.Join(custom, "data"), "XDG_STATE_HOME": filepath.Join(custom, "state"), "XDG_CONFIG_HOME": filepath.Join(custom, "config"), "OPENCODE_DB": "custom.db"}
	m.host.Env = func(key string) string { return values[key] }
	scope, err := m.scope()
	if err != nil || scope.Database != filepath.Join(custom, "data", "opencode", "custom.db") || scope.State != filepath.Join(custom, "state", "opencode") {
		t.Fatal("host scope", err)
	}
	all, err := m.ListNative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Activate(context.Background(), "opencode-go", all[1].Credential); err != nil {
		t.Fatal(err)
	}
	values["OPENCODE_DB"] = ":memory:"
	if _, err = m.Capture(context.Background(), ""); !errors.Is(err, ErrUnsupported) {
		t.Fatal("accepted ephemeral reference")
	}
}

func TestOpenCodeRefSurvivesExecutableUpgrade(t *testing.T) {
	m := openCodeTestManager(t)
	all, err := m.ListNative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(m.host.Executable)
	if err != nil {
		t.Fatal(err)
	}
	upgraded := filepath.Join(t.TempDir(), "opencode-new-version")
	if err = os.WriteFile(upgraded, script, 0700); err != nil {
		t.Fatal(err)
	}
	m.host.Executable = upgraded
	previous, err := m.Activate(context.Background(), "opencode-go", all[1].Credential)
	if err != nil || len(previous) != 1 {
		t.Fatal("executable upgrade invalidated saved reference", err)
	}
	// The saved runtime paths are not credential identity. Only data scope binds it.
	ref, err := openCodeUnpack(all[1].Credential)
	if err != nil {
		t.Fatal(err)
	}
	ref.Scope.Cache = filepath.Join(t.TempDir(), "old-cache")
	ref.Scope.State = filepath.Join(t.TempDir(), "old-state")
	c := all[1].Credential
	c.Payload, _ = json.Marshal(map[string]openCodeRef{"ref": ref})
	if _, err = m.Activate(context.Background(), "opencode-go", c); err != nil {
		t.Fatal("runtime paths invalidated saved reference", err)
	}
}

func TestOpenCodeCaptureLabelsProviders(t *testing.T) {
	for _, tc := range []struct{ integration, label string }{
		{"opencode-go", "OpenCode Go"},
		{"opencode-go", "OpenCode Go"},
		{"opencode", "OpenCode Zen"},
	} {
		t.Run(tc.integration, func(t *testing.T) {
			captured := openCodeCaptured(openCodeScope{}, tc.integration, "credential-id")
			if captured.Label != tc.label || captured.Integration != tc.integration || captured.Identity != "opencode:"+tc.integration+":credential-id" {
				t.Fatalf("provider label changed identity: %+v", captured)
			}
		})
	}
}

func TestOpenCodeSkipsOtherProviderCredentials(t *testing.T) {
	m := openCodeTestManager(t)
	path := filepath.Join(m.host.Home, "list.json")
	raw := `[{"id":"openai","connections":[{"type":"credential","id":"native_openai"}]},{"id":"xai","connections":[{"type":"credential","id":"native_xai"}]},{"id":"opencode-go","connections":[{"type":"credential","id":"go"}]},{"id":"opencode","connections":[{"type":"credential","id":"zen"}]}]`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{false, true} {
		rows, err := m.captures(context.Background(), all)
		if err != nil || len(rows) != 2 || rows[0].Label != "OpenCode Go" || rows[1].Label != "OpenCode Zen" {
			t.Fatalf("unexpected accounts: %+v, %v", rows, err)
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != raw {
		t.Fatal("capture changed native credentials")
	}
	if _, _, err := m.LoginCommand("", "xai"); err == nil {
		t.Fatal("accepted provider login")
	}
}

func TestOpenCodeUnusableActiveCredentialRequiresSignIn(t *testing.T) {
	for _, status := range []string{`{"code":"needs_auth"}`, `{"code":"error"}`} {
		t.Run(status, func(t *testing.T) {
			m := openCodeTestManager(t)
			raw := `[{"id":"opencode-go","connections":[{"type":"credential","id":"expired","status":` + status + `},{"type":"credential","id":"saved"}]}]`
			if err := os.WriteFile(filepath.Join(m.host.Home, "list.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			rows, err := m.Capture(context.Background(), "")
			if !errors.Is(err, ErrNotSignedIn) || len(rows) != 0 {
				t.Fatalf("unusable active reference accepted: %d rows, %v", len(rows), err)
			}
			saved, err := m.ListNative(context.Background())
			if err != nil || len(saved) != 2 {
				t.Fatalf("saved references lost: %+v %v", saved, err)
			}
			after, err := os.ReadFile(filepath.Join(m.host.Home, "list.json"))
			if err != nil || string(after) != raw {
				t.Fatal("status check changed native credentials", err)
			}
		})
	}
}

func TestOpenCodeCapturePreservesCancellation(t *testing.T) {
	m := openCodeTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Capture(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestOpenCodeCaptureReportsStaticKey(t *testing.T) {
	m := openCodeTestManager(t)
	script, err := os.ReadFile(m.host.Executable)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.Replace(string(script), `elif [ "$1 $2" = "auth switch" ]; then`, `elif [ "$1 $2" = "auth export" ]; then
 echo '[{"id":"cred_old","integrationID":"opencode-go","value":{"type":"key","key":"fixture-secret"}}]'
 elif [ "$1 $2" = "auth switch" ]; then`, 1))
	if err := os.WriteFile(m.host.Executable, script, 0700); err != nil {
		t.Fatal(err)
	}
	rows, err := m.Capture(t.Context(), "")
	if err != nil || len(rows) == 0 {
		t.Fatal(err)
	}
	if !CredentialUsesAPIKey(rows[0]) {
		t.Fatal("OpenCode static key not identified")
	}
	if strings.Contains(string(rows[0].Credential.Payload), "fixture-secret") {
		t.Fatal("reference copied secret")
	}
}

func TestOpenCodeReferenceExpiry(t *testing.T) {
	s := openCodeScope{Home: t.TempDir()}
	c := openCodeCaptured(s, "opencode-go", "fixture")
	ref, _ := openCodeUnpack(c.Credential)
	ref.CredentialType = "oauth"
	ref.Expires = 1800000000000
	c.Credential.Payload, _ = json.Marshal(map[string]openCodeRef{"ref": ref})
	if got := CredentialExpiry(c.Credential); got.UnixMilli() != ref.Expires {
		t.Fatalf("expiry = %v", got)
	}
	if got, err := (&openCodeManager{}).CredentialExpiry(c); err != nil || got.UnixMilli() != ref.Expires {
		t.Fatalf("manager expiry = %v, %v", got, err)
	}
	if CredentialUsesAPIKey(c) {
		t.Fatal("OAuth login reported as static key")
	}
}

func TestOpenCodeUsageRequiresNativeAuthExport(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         error
	}{
		{"command failure", "echo private-error >&2; exit 1", errors.New("OpenCode account command failed.")},
		{"malformed output", "echo malformed", ErrSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := openCodeTestManager(t)
			s, err := m.scope()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.Executable, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(s.Database), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.Database, nil, 0600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			marker := filepath.Join(bin, "invoked")
			if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte("#!/bin/sh\necho invoked > '"+marker+"'\necho '[]'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			got, err := m.usageCredentials(context.Background(), s, "opencode-go")
			if got != nil || err == nil || err.Error() != tc.want.Error() {
				t.Errorf("credentials = %v, error = %v; want nil and %v", got, err, tc.want)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Error("sqlite3 fallback ran")
			}
		})
	}
}
