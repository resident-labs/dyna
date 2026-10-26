package computers

import (
	"dyna-agent/internal/testsafe"
	"path/filepath"
	"testing"
)

func TestBrowserEnvironmentScopesDaemonsAndProfiles(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	runtime := testsafe.ShortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	base := []string{"PATH=/usr/bin", "XDG_RUNTIME_DIR=" + runtime, "AGENT_BROWSER_SOCKET_DIR=/owner/shared", "AGENT_BROWSER_NAMESPACE=owner", "AGENT_BROWSER_SESSION=owner", "AGENT_BROWSER_PROFILE=/owner/profile", "AGENT_BROWSER_CDP=9222", "AGENT_BROWSER_AUTO_CONNECT=1", "AGENT_BROWSER_DAEMON=1"}
	first, err := Environment(Meta{SessionID: "first", WaylandDisplay: "first-display", Display: ":234"}, base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Environment(Meta{SessionID: "second", WaylandDisplay: "second-display", Display: ":235"}, first)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"AGENT_BROWSER_SOCKET_DIR", "AGENT_BROWSER_NAMESPACE"} {
		if envValue(first, key) == envValue(second, key) || envValue(first, key) == envValue(base, key) {
			t.Fatalf("shared %s", key)
		}
	}
	if envValue(first, "AGENT_BROWSER_PROFILE") != computerBrowserProfile("first") || envValue(second, "AGENT_BROWSER_PROFILE") != computerBrowserProfile("second") {
		t.Fatal("browser profile not scoped")
	}
	for _, env := range [][]string{first, second} {
		for _, key := range []string{"AGENT_BROWSER_CDP", "AGENT_BROWSER_DAEMON", "AGENT_BROWSER_AUTO_CONNECT"} {
			if envValue(env, key) != "" {
				t.Fatalf("inherited %s", key)
			}
		}
	}
	socket := filepath.Join(browserSocketDir("/run/user/1000", "ses_20261017-155310-89387fa5"), "namespaces", browserNamespace("ses_20261017-155310-89387fa5"), "run", "default.sock")
	if len(socket) > 103 {
		t.Fatalf("agent-browser socket too long: %s", socket)
	}
}
