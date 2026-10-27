package computers

import (
	"slices"
	"strings"
	"testing"
)

// An app in the computer must reach the computer's own session bus, never the
// host's, or its file chooser opens on the host display (D-274).
func TestDesktopEnvUsesTheComputersOwnSessionBus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	s := &supervisor{privateWayland: "wayland-private", privateDisplay: ":9", busAddress: "unix:path=/run/dyna/bus"}
	env := s.desktopEnv()
	if got := envValue(env, "DBUS_SESSION_BUS_ADDRESS"); got != "unix:path=/run/dyna/bus" {
		t.Fatalf("app bus = %q, want the computer's own bus, not the host's", got)
	}
	if got := envValue(env, "WAYLAND_DISPLAY"); got != "wayland-private" {
		t.Fatalf("app display = %q", got)
	}
}

// Without a private bus the app keeps the environment it had.
func TestDesktopEnvWithoutABusKeepsTheHostEnvironment(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	s := &supervisor{privateWayland: "wayland-private"}
	if got := envValue(s.desktopEnv(), "DBUS_SESSION_BUS_ADDRESS"); got != "unix:path=/run/user/1000/bus" {
		t.Fatalf("bus = %q, want the inherited value when the computer has no bus", got)
	}
}

// The bus and the portal it activates inherit the private display and name the
// bus, so the portal backend draws inside the computer.
func TestBusLaunchSetup(t *testing.T) {
	address := "unix:path=/run/dyna/bus"
	cmd := busCommand(address)
	if cmd[0] != "dbus-daemon" || !slices.Contains(cmd, "--session") || !slices.Contains(cmd, "--nofork") || !slices.Contains(cmd, "--address="+address) {
		t.Fatalf("bus command = %v", cmd)
	}
	host := []string{"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus", "WAYLAND_DISPLAY=wayland-private", "DISPLAY=:9", "XDG_RUNTIME_DIR=/run/user/1000"}
	env := busEnvironment(host, address)
	for want, key := range map[string]string{address: "DBUS_SESSION_BUS_ADDRESS", "wayland-private": "WAYLAND_DISPLAY", ":9": "DISPLAY", "/run/user/1000": "XDG_RUNTIME_DIR", "labwc": "XDG_CURRENT_DESKTOP"} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(strings.Join(env, "\n"), "/run/user/1000/bus") {
		t.Error("the host bus address leaked into the private bus's environment")
	}
}

// Every launch path that builds an app's environment (computer open, run, env
// and the browser all use Environment) names the computer's own bus, even while
// the lazy desktop has not started its bus yet. Naming the host's bus then sent
// file choosers to the host display (D-274).
func TestEnvironmentNamesTheComputersBusBeforeItExists(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	m := Meta{SessionID: "bus-test", WaylandDisplay: "private", Display: ":9"}
	base := []string{"PATH=/usr/bin", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}
	env, err := Environment(m, base)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := envValue(env, "DBUS_SESSION_BUS_ADDRESS"), "unix:path="+BusSocket(m.SessionID); got != want {
		t.Fatalf("bus = %q, want the computer's own %q", got, want)
	}
}
