package computers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func browserNamespace(id string) string { return strings.TrimPrefix(publicName(id), "dyna-computer-") }
func browserSocketDir(runtime, id string) string {
	// Keep Unix socket paths short, including agent-browser's namespace suffix.
	return filepath.Join(runtime, "dyna", "b-"+browserNamespace(id))
}

// Scope automation daemons as well as browser processes. Porch invokes its
// bundled agent-browser by absolute path, so PATH shims alone cannot do this.
func browserEnvironment(m Meta, base []string) ([]string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return nil, fmt.Errorf("computer browser isolation requires XDG_RUNTIME_DIR")
	}
	values := map[string]string{
		"AGENT_BROWSER_SOCKET_DIR": browserSocketDir(runtime, m.SessionID),
		"AGENT_BROWSER_NAMESPACE":  browserNamespace(m.SessionID),
		"AGENT_BROWSER_SESSION":    "default",
		"AGENT_BROWSER_NO_XVFB":    "1",
		"AGENT_BROWSER_PROFILE":    computerBrowserProfile(m.SessionID),
	}
	values["DYNA_CHROMIUM_EXECUTABLE"] = chromiumWrapper(m.SessionID)
	values["DYNA_CHROMIUM_FLAGS_FILE"] = chromiumFlagsFile(m.SessionID)
	values["AGENT_BROWSER_EXECUTABLE_PATH"] = chromiumWrapper(m.SessionID)
	// An inherited CDP endpoint or daemon marker must not attach to an owner
	// browser or turn a CLI invocation into an existing daemon.
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if key == "AGENT_BROWSER_CDP" || key == "AGENT_BROWSER_DAEMON" || key == "AGENT_BROWSER_AUTO_CONNECT" || key == "AGENT_BROWSER_PROFILE" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return mergeEnvironment(filtered, values), nil
}
