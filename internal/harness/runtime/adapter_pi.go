package runtime

import (
	"os"
	"path/filepath"
)

// piNPMPackage is the official Pi 1.1+ package name. Dyna packages its own
// fork under this name; older pinned bundles can still use the npm release.
const piNPMPackage = "@earendil-works/pi-coding-agent"

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:      "pi",
		Binary:       "pi",
		ConfigFormat: "json",
		MCPKey:       "mcpServers",
		ConfigFile:   "settings.json",
		// Pi loads $PI_CODING_AGENT_DIR/AGENTS.md as global rules.
		NativeEnv:  piNativeEnv,
		ConfigArgs: piConfigArgs,
		LoginArgs:  piLoginArgs,
		// Pi has no account of its own. /login inside Pi stores provider OAuth
		// grants and API keys in auth.json. Dyna runs each account from its own
		// home, so a provider sign-in becomes an account of that provider.
		AuthFiles: []string{"auth.json"},
		// Account sign-in is not blocked: /login stores credentials in the
		// private auth.json, so no shared credential backend is touched.
		AccountBlocked: "",
		// Turns share the account lease. Verified 2026-10-21 on Pi 1.3.0: two
		// `pi --model zai/glm-5.3-flash -p` runs in one PI_CODING_AGENT_DIR both
		// exited 0 side by side, and a third turn ran in the same home. Pi writes
		// each session to its own file under sessions/, and auth.json holds a
		// static API key. An exclusive lease queued new turns behind a long one.
	})
}

// piNativeEnv scopes Pi to one private home. The agent directory covers
// settings, mcp.json, auth.json and user instructions; a turn overrides the
// agent directory with the account's own home. Shared launch code supplies
// private HOME/XDG roots; PI_OFFLINE stops automatic network activity.
func piNativeEnv(home string) map[string]string {
	env := map[string]string{
		piAgentDirKey:   home,
		piSessionDirKey: filepath.Join(home, "sessions"),
		"PI_OFFLINE":    "1",
		// PI_SKIP_VERSION_CHECK stops the pi.dev latest-version request.
		"PI_SKIP_VERSION_CHECK": "1",
		// Pi deletes inherited provider credentials and endpoints before auth
		// resolution, so its tools and MCP servers inherit the clean environment.
		"PI_NO_ENV_CREDENTIALS": "1",
	}
	return env
}

// piConfigArgs pins sessions into the private home and points Pi at the
// generation's own config files. Pi reads one agent directory, so these flags
// keep each generation's settings and MCP servers per process; the account
// home holds only the login. --session-dir takes precedence over the
// sessionDir setting and environment, and the models flag joins the owner's
// shared definitions when they exist.
func piConfigArgs(home, file string) []string {
	args := []string{
		"--session-dir", filepath.Join(home, "sessions"),
		"--settings", file,
		"--mcp-config", filepath.Join(filepath.Dir(file), "pi-mcp.json"),
	}
	if _, err := os.Stat(filepath.Join(home, "models.json")); err == nil {
		args = append(args, "--models-file", filepath.Join(home, "models.json"))
	}
	return args
}

// piLoginArgs returns a complete argv for the interactive sign-in flow. Pi has
// no non-interactive login command; the user runs /login in the terminal UI and
// Pi stores the credential in <agent-dir>/auth.json inside the private home.
func piLoginArgs(executable, home string) []string {
	return []string{executable}
}
