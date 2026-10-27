// Package shelldisplay builds the display environment of a managed shell. A
// shell runs on the owner's own display only for the owner's terminals, the
// internal root session, and a session that root or the owner granted
// OwnerDisplay. Every other shell uses its session computer when one already
// runs, and otherwise gets an explicit dead display, so a GUI program fails at
// once instead of reaching the owner's desktop.
package shelldisplay

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
)

const (
	// ownerDisplayKey and ownerWaylandKey hold the owner's own display values.
	// computers.Environment saves them before it routes a session away from them.
	ownerDisplayKey = "DYNA_OWNER_DISPLAY"
	ownerWaylandKey = "DYNA_OWNER_WAYLAND_DISPLAY"
	// rootRole is subagent.RoleRoot as a stored session record holds it. This
	// package cannot import subagent, because subagent imports shells.
	rootRole = "root"
)

// chromeUserDataEnv and chromeConfigEnv keep Chromium from joining a browser
// of the account's shared config. Chromium tells an existing process apart by
// its user data and config directories before it needs a display, so a shell
// that shares them can reach a browser running on the owner display.
const (
	chromeUserDataEnv = "CHROME_USER_DATA_DIR"
	chromeConfigEnv   = "CHROME_CONFIG_HOME"
)

// runningComputer reads a running computer's display environment. A test
// replaces it, so its shells need no desktop.
var runningComputer = computers.RunningEnvironment

// sessionDisplay is the part of a stored session record that decides whether
// its shells keep the owner display. The key ownerDisplay is written by the
// session grant; a record without it keeps no grant.
type sessionDisplay struct {
	Role         string `json:"role"`
	OwnerDisplay bool   `json:"ownerDisplay"`
}

// computerBrowserKeys and computerBrowserVars carry a session's computer
// browser to its child processes. An owner-display or isolated shell drops
// them, because they can attach to, or start, a computer.
var (
	computerBrowserKeys = []string{"AGENT_BROWSER_", "DYNA_CHROMIUM_"}
	computerBrowserVars = []string{"DYNA_COMPUTER_PROCESS"}
)

// CommandEnv builds the environment of a managed shell's command, and reports
// whether the shell has no display, so the job needs a hint where GUI programs
// can run.
func CommandEnv(ctx context.Context, dir string, m shells.Meta, base []string) ([]string, bool) {
	if ownerDisplayStays(m, base) {
		return ownerDisplayEnv(base, m.Owner), false
	}
	if env, ok := runningComputerEnv(ctx, m, base); ok {
		// computers.Environment keeps the owner bus and owner displays. A
		// shell blocks them on purpose: a D-Bus singleton must not activate an
		// owner app, a connected Wayland fd wins over WAYLAND_DISPLAY, and the
		// hypruse shim would restore the owner displays from saved values.
		env = envSet(env, "DBUS_SESSION_BUS_ADDRESS", noSessionBus(dir))
		for _, key := range []string{"WAYLAND_SOCKET", ownerDisplayKey, ownerWaylandKey, "HYPRLAND_INSTANCE_SIGNATURE"} {
			env = envSet(env, key, "")
		}
		// An inherited owner profile would let Chromium forward to a browser
		// that already runs, before it looks for a display. The computer's own
		// profile is already private, so the shell joins that browser instead.
		env = envSet(env, chromeUserDataEnv, computers.BrowserProfileDir(m.Owner))
		return envSet(env, chromeConfigEnv, computers.BrowserConfigDir(m.Owner)), false
	}
	return isolatedDisplay(dir, base, m.Owner), true
}

// ownerDisplayStays reports whether a shell runs on the owner's own display.
// A shell with no session owner is an interactive owner terminal, which keeps
// its environment as it is.
func ownerDisplayStays(m shells.Meta, base []string) bool {
	if !runstore.IsSessionDirID(m.Owner) {
		return true
	}
	keep, err := ownerDisplayGrant(m.Owner)
	// An unreadable record keeps no display: a dead display is a clear error.
	return err == nil && keep
}

// ownerDisplayGrant reports whether a stored session always keeps the owner
// display: the internal root, or a session root or the owner granted it.
func ownerDisplayGrant(id string) (bool, error) {
	var d sessionDisplay
	if err := sessionlife.Load(runstore.SessionsDir(), id, &d); err != nil {
		return false, err
	}
	return d.OwnerDisplay || d.Role == rootRole, nil
}

// runningComputerEnv returns the display environment of the session's running
// computer, and false when the session has none or no policy is available.
func runningComputerEnv(ctx context.Context, m shells.Meta, base []string) ([]string, bool) {
	if runningComputer == nil || !runstore.IsSessionDirID(m.Owner) {
		return nil, false
	}
	return runningComputer(ctx, m.Owner, base)
}

// ownerDisplayEnv puts the owner's display back after a harness routed the
// session to its computer, and drops that computer's browser handoff. A saved
// value that is empty, or absent after injection, clears the display: the
// owner had none. An environment with no saved value was never routed to a
// session computer, so it already holds its own display.
func ownerDisplayEnv(base []string, computer string) []string {
	if !computerInjected(base) {
		return slices.Clone(base)
	}
	out := withoutComputerBrowser(base, computer)
	out = envSet(out, "WAYLAND_DISPLAY", value(base, ownerWaylandKey))
	out = envSet(out, "DISPLAY", value(base, ownerDisplayKey))
	// The computer's bus is a socket this shell does not use. An injection from
	// before the owner bus was saved leaves the bus as it is.
	if bus, ok := envLookup(base, computers.OwnerBusKey); ok {
		out = envSet(out, "DBUS_SESSION_BUS_ADDRESS", bus)
	}
	return out
}

// computerInjected reports whether computers.Environment routed this
// environment, which always saves both owner displays.
func computerInjected(base []string) bool {
	if _, ok := envLookup(base, ownerDisplayKey); ok {
		return true
	}
	_, ok := envLookup(base, ownerWaylandKey)
	return ok
}

// isolatedDisplay removes owner display routing from a shell whose computer is
// not running. XDG_RUNTIME_DIR stays, so dyna computer open and dyna computer
// run still reach the computer control socket under it. An absolute, missing
// Wayland socket overrides the wayland-0 default there, and an explicit
// unusable session bus address stops the bus from being found at
// /run/user/<uid>/bus.
func isolatedDisplay(dir string, base []string, computer string) []string {
	out := withoutComputerBrowser(base, computer)
	for _, key := range []string{
		"DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "XDG_SESSION_TYPE",
		"XDG_CURRENT_DESKTOP", "HYPRLAND_INSTANCE_SIGNATURE", ownerDisplayKey, ownerWaylandKey,
	} {
		out = envSet(out, key, "")
	}
	out = envSet(out, "WAYLAND_DISPLAY", filepath.Join(dir, "no-wayland-display"))
	out = envSet(out, "DBUS_SESSION_BUS_ADDRESS", noSessionBus(dir))
	// A private Chromium profile keeps an absolute browser launch, such as an
	// xdg-open desktop entry, from forwarding to the account's shared browser.
	out = envSet(out, chromeUserDataEnv, filepath.Join(dir, "chrome-user-data"))
	return envSet(out, chromeConfigEnv, filepath.Join(dir, "chrome-config"))
}

// noSessionBus is an explicit session bus address that nothing listens on. An
// unset address would let libdbus and GDBus fall back to /run/user/<uid>/bus.
func noSessionBus(dir string) string {
	return "unix:path=" + filepath.Join(dir, "no-session-bus")
}

// withoutComputerBrowser drops the browser shims and browser handoff of a
// session computer, so a shell never reuses or starts one by accident.
func withoutComputerBrowser(env []string, computer string) []string {
	out := make([]string, 0, len(env))
	for _, line := range env {
		key, val, _ := strings.Cut(line, "=")
		if hasAnyPrefix(key, computerBrowserKeys) || slices.Contains(computerBrowserVars, key) {
			continue
		}
		if key == "PATH" {
			val = withoutShimDirs(val, computer)
		}
		out = append(out, key+"="+val)
	}
	return out
}

func withoutShimDirs(path, computer string) string {
	kept := make([]string, 0, len(filepath.SplitList(path)))
	for _, dir := range filepath.SplitList(path) {
		if dir != "" && !isShimDir(dir, computer) {
			kept = append(kept, dir)
		}
	}
	return strings.Join(kept, string(filepath.ListSeparator))
}

// isShimDir reports whether dir is a computer's shim directory. A shell's
// PATH can name the shims of its own computer after that computer stopped,
// and a shim may ask the computer service to start a desktop.
func isShimDir(dir, computer string) bool {
	return computer != "" && filepath.Clean(dir) == filepath.Clean(computers.ShimDir(computer))
}

func hasAnyPrefix(key string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// envLookup reads one key and reports whether the environment names it at all.
func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, line := range env {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

// value reads one key, or "" when the key is absent.
func value(env []string, key string) string {
	got, _ := envLookup(env, key)
	return got
}

// envSet replaces one key, and removes it when value is empty.
func envSet(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, line := range env {
		if !strings.HasPrefix(line, prefix) {
			out = append(out, line)
		}
	}
	if val != "" {
		out = append(out, prefix+val)
	}
	return out
}
