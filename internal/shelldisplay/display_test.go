package shelldisplay

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/shells"
)

// testSessionRecord is the stored shape this policy reads. It mirrors the
// grant builder's fields, so a renamed JSON key fails the test.
type testSessionRecord struct {
	ID           string `json:"id"`
	Role         string `json:"role,omitempty"`
	OwnerDisplay bool   `json:"ownerDisplay,omitempty"`
}

// desktopBase is a harness environment after computer injection: the session
// computer's display, the owner display saved beside it, and browser handoff.
func desktopBase(shim string) []string {
	return []string{
		"HOME=/home/owner",
		"PATH=/usr/local/bin:" + shim + ":/usr/bin:/bin",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DISPLAY=:234",
		"WAYLAND_DISPLAY=dyna-public",
		"DYNA_OWNER_DISPLAY=:0",
		"DYNA_OWNER_WAYLAND_DISPLAY=wayland-owner",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"AGENT_BROWSER_SOCKET_DIR=/run/user/1000/dyna/b-x",
		"AGENT_BROWSER_PROFILE=/data/browser/x",
		"DYNA_CHROMIUM_EXECUTABLE=/data/computers/x/bin/chromium",
		"WAYLAND_SOCKET=13",
		"HYPRLAND_INSTANCE_SIGNATURE=owner-hypr",
		"CHROME_USER_DATA_DIR=/home/owner/.config/chromium",
		"CHROME_CONFIG_HOME=/home/owner/.config",
		"DYNA_COMPUTER_PROCESS=ses_parent",
	}
}

// TestCommandEnv guards every way a managed shell reaches a display: an
// unavailable computer, a running one, a root or granted session, a workflow
// worker, and the owner's own terminal.
func TestCommandEnv(t *testing.T) {
	ctx := context.Background()
	const owner = "ses_20261025-090000-abcdef12"
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "sh_20261025-090000-abcdef12")
	shim := computers.ShimDir(owner)
	meta := shells.Meta{ID: "sh_20261025-090000-abcdef12", Owner: owner}

	writeSession := func(t *testing.T, rec testSessionRecord) {
		t.Helper()
		recordtest.Write(t, runstore.SessionsDir(), rec.ID, rec)
	}
	noComputer := func(context.Context, string, []string) ([]string, bool) { return nil, false }
	running := func(_ context.Context, id string, base []string) ([]string, bool) {
		if id != owner {
			t.Fatalf("computer lookup for %q, want %q", id, owner)
		}
		return envSet(envSet(base, "DISPLAY", ":240"), "WAYLAND_DISPLAY", "wayland-computer"), true
	}
	use := func(t *testing.T, lookup func(context.Context, string, []string) ([]string, bool)) {
		t.Helper()
		saved := runningComputer
		runningComputer = lookup
		t.Cleanup(func() { runningComputer = saved })
	}

	t.Run("no computer blocks the owner display and bus", func(t *testing.T) {
		use(t, noComputer)
		env, noDisplay := CommandEnv(ctx, dir, meta, desktopBase(shim))
		if !noDisplay {
			t.Fatal("noDisplay = false; a shell without a computer must say where GUI apps run")
		}
		for _, key := range []string{"DISPLAY", "WAYLAND_SOCKET", "XDG_SESSION_TYPE", "DYNA_OWNER_DISPLAY", "DYNA_OWNER_WAYLAND_DISPLAY", "AGENT_BROWSER_PROFILE", "AGENT_BROWSER_SOCKET_DIR", "DYNA_CHROMIUM_EXECUTABLE", "DYNA_COMPUTER_PROCESS"} {
			if got, ok := envLookup(env, key); ok {
				t.Errorf("%s = %q, want the key absent", key, got)
			}
		}
		if got, want := value(env, "WAYLAND_DISPLAY"), filepath.Join(dir, "no-wayland-display"); got != want {
			t.Errorf("WAYLAND_DISPLAY = %q, want %q", got, want)
		}
		if got, want := value(env, "DBUS_SESSION_BUS_ADDRESS"), noSessionBus(dir); got != want {
			t.Errorf("DBUS_SESSION_BUS_ADDRESS = %q, want %q", got, want)
		}
		// The control plane stays reachable, so dyna computer open and run work.
		if got := value(env, "XDG_RUNTIME_DIR"); got != "/run/user/1000" {
			t.Errorf("XDG_RUNTIME_DIR = %q, want the runtime directory kept", got)
		}
		if got := value(env, "PATH"); strings.Contains(got, shim) || !strings.Contains(got, "/usr/bin") {
			t.Errorf("PATH = %q, want the shim dropped and the rest kept", got)
		}
		// A private browser profile stops Chromium forwarding to the account's
		// shared browser before it looks for a display.
		if got, want := value(env, chromeUserDataEnv), filepath.Join(dir, "chrome-user-data"); got != want {
			t.Errorf("CHROME_USER_DATA_DIR = %q, want %q", got, want)
		}
		if got, want := value(env, chromeConfigEnv), filepath.Join(dir, "chrome-config"); got != want {
			t.Errorf("CHROME_CONFIG_HOME = %q, want %q", got, want)
		}
	})

	t.Run("running computer keeps its display and blocks the owner bus", func(t *testing.T) {
		use(t, running)
		env, noDisplay := CommandEnv(ctx, dir, meta, desktopBase(shim))
		if noDisplay {
			t.Fatal("noDisplay = true; a running computer needs no hint")
		}
		if got := value(env, "DISPLAY"); got != ":240" {
			t.Errorf("DISPLAY = %q, want the computer display", got)
		}
		if got, want := value(env, "DBUS_SESSION_BUS_ADDRESS"), noSessionBus(dir); got != want {
			t.Errorf("DBUS_SESSION_BUS_ADDRESS = %q, want %q", got, want)
		}
		for _, key := range []string{"WAYLAND_SOCKET", ownerDisplayKey, ownerWaylandKey, "HYPRLAND_INSTANCE_SIGNATURE"} {
			if got, ok := envLookup(env, key); ok {
				t.Errorf("%s = %q, want the key absent", key, got)
			}
		}
		// The computer's own private profile, so an absolute Chromium launch
		// joins that browser instead of one of the shared account config.
		if got, want := value(env, chromeUserDataEnv), computers.BrowserProfileDir(owner); got != want {
			t.Errorf("CHROME_USER_DATA_DIR = %q, want %q", got, want)
		}
		if got, want := value(env, chromeConfigEnv), computers.BrowserConfigDir(owner); got != want {
			t.Errorf("CHROME_CONFIG_HOME = %q, want %q", got, want)
		}
	})

	t.Run("root session restores the owner display", func(t *testing.T) {
		use(t, noComputer)
		writeSession(t, testSessionRecord{ID: owner, Role: rootRole})
		env, noDisplay := CommandEnv(ctx, dir, meta, desktopBase(shim))
		if noDisplay {
			t.Fatal("noDisplay = true; root keeps the owner display")
		}
		if got := value(env, "DISPLAY"); got != ":0" {
			t.Errorf("DISPLAY = %q, want the owner display :0", got)
		}
		if got := value(env, "WAYLAND_DISPLAY"); got != "wayland-owner" {
			t.Errorf("WAYLAND_DISPLAY = %q, want wayland-owner", got)
		}
		if got := value(env, "PATH"); strings.Contains(got, shim) {
			t.Errorf("PATH = %q, want the session computer shim dropped", got)
		}
		if _, ok := envLookup(env, "AGENT_BROWSER_PROFILE"); ok {
			t.Error("root kept the session computer browser handoff")
		}
		if got := value(env, "DBUS_SESSION_BUS_ADDRESS"); got != "unix:path=/run/user/1000/bus" {
			t.Errorf("DBUS_SESSION_BUS_ADDRESS = %q, want the owner bus kept", got)
		}
		// Root keeps its browser values: it runs on the owner display.
		if got := value(env, chromeUserDataEnv); got != "/home/owner/.config/chromium" {
			t.Errorf("CHROME_USER_DATA_DIR = %q, want the owner value kept for root", got)
		}
	})

	t.Run("granted session restores the owner display", func(t *testing.T) {
		use(t, noComputer)
		writeSession(t, testSessionRecord{ID: owner, OwnerDisplay: true})
		env, noDisplay := CommandEnv(ctx, dir, meta, desktopBase(shim))
		if noDisplay || value(env, "DISPLAY") != ":0" {
			t.Fatalf("granted shell noDisplay = %v, DISPLAY = %q; want the owner display", noDisplay, value(env, "DISPLAY"))
		}
	})

	t.Run("empty saved owner display clears a root display", func(t *testing.T) {
		use(t, noComputer)
		writeSession(t, testSessionRecord{ID: owner, Role: rootRole})
		base := []string{"DISPLAY=:234", "DYNA_OWNER_DISPLAY=", "WAYLAND_DISPLAY=dyna-public"}
		env, _ := CommandEnv(ctx, dir, meta, base)
		if got, ok := envLookup(env, "DISPLAY"); ok {
			t.Errorf("DISPLAY = %q, want the key absent; the owner had no display", got)
		}
	})

	t.Run("workflow worker keeps no owner display exemption", func(t *testing.T) {
		writeSession(t, testSessionRecord{ID: owner, Role: rootRole})
		base := append(desktopBase(shim), mode.EnvMode+"="+mode.Workflow)
		// A running computer of the attributed session is still usable; the
		// owner display exemption of its root session is not.
		use(t, running)
		env, _ := CommandEnv(ctx, dir, meta, base)
		if got := value(env, "DISPLAY"); got != ":240" {
			t.Errorf("DISPLAY = %q, want the computer display", got)
		}
		if got := value(env, "WAYLAND_DISPLAY"); got != "wayland-computer" {
			t.Errorf("WAYLAND_DISPLAY = %q, want the computer display, not the owner one", got)
		}
		// Without a computer the same worker is isolated.
		use(t, noComputer)
		env, noDisplay := CommandEnv(ctx, dir, meta, base)
		if !noDisplay {
			t.Fatal("noDisplay = false; a workflow worker without a computer has no display")
		}
		if got, ok := envLookup(env, "DISPLAY"); ok {
			t.Errorf("DISPLAY = %q, want the key absent", got)
		}
	})

	t.Run("owner terminal keeps its environment unchanged", func(t *testing.T) {
		use(t, noComputer)
		base := []string{"PATH=/usr/bin:/bin", "DISPLAY=:0", "WAYLAND_DISPLAY=wayland-owner", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
		env, noDisplay := CommandEnv(ctx, dir, shells.Meta{ID: meta.ID}, base)
		if noDisplay || !slices.Equal(env, base) {
			t.Fatalf("owner shell noDisplay = %v, env = %v; want it unchanged", noDisplay, env)
		}
	})
}
