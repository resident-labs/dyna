package hostexec

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"github.com/resident-labs/dyna/contract"
)

const OutputLimit = 8 << 20

// CollectTimeout bounds reading back a run's $DYNA_OUT.
const CollectTimeout = contract.DurationMS(2 * 60 * 1000)

// desktopVars are what a run on an exec-only host takes from the user's graphical session, so GUI commands work over SSH.
const desktopVars = "WAYLAND_DISPLAY=*|HYPRLAND_INSTANCE_SIGNATURE=*|DBUS_SESSION_BUS_ADDRESS=*|XDG_RUNTIME_DIR=*|DISPLAY=*"

// podmanGuard is the shim a run puts first on its PATH (spec 6.3).
//
//go:embed podman-guard.sh
var podmanGuard []byte

// OutDir is where a run's $DYNA_OUT lives on the host.
func OutDir(op contract.OpID) string { return `${TMPDIR:-/tmp}/dyna-out-` + string(op) }

// prelude makes a run's $DYNA_OUT and, for a desktop run, imports the session environment. A collect run has neither.
func prelude(a contract.HostexecRunArgs, op contract.OpID) string {
	if a.Collect {
		return ""
	}
	s := `d=` + OutDir(op) + `; mkdir -m 0700 -- "$d" || exit 254; export DYNA_OUT=$d; `
	s += `g=$d.bin; mkdir -m 0700 -- "$g" || exit 254; printf %s ` + base64.StdEncoding.EncodeToString(podmanGuard) + ` | base64 -d > "$g/podman"; chmod 0700 "$g/podman"; command -v docker >/dev/null && ln -s podman "$g/docker"; export PATH=$g:$PATH; `
	if a.Desktop {
		s += `while IFS= read -r l; do case $l in ` + desktopVars + `) export "$l";; esac; done < <(systemctl --user show-environment 2>/dev/null); `
	}
	return s
}

// collectScript prints a tar of the files a run left in $DYNA_OUT, or nothing when there are none, and removes the folder.
func collectScript(op contract.OpID) string {
	d := OutDir(op)
	return `d=` + d + `; rc=0; if [ -n "$(find "$d" -type f -print -quit 2>/dev/null)" ]; then tar -C "$d" -cf - . || rc=3; fi; rm -rf -- "$d" "$d.bin" || rc=3; exit $rc`
}

// CollectArgs is the run that reads back and removes a finished run's $DYNA_OUT.
func CollectArgs(a contract.HostexecRunArgs, op contract.OpID) contract.HostexecRunArgs {
	a.Argv, a.Cwd, a.Desktop, a.Collect, a.Timeout = []string{"bash", "-c", collectScript(op)}, "", false, true, CollectTimeout
	return a
}

func RunArgs(a contract.HostexecRunArgs, op contract.OpID) []string {
	cwd := a.Cwd
	if cwd == "" {
		cwd = "."
	}
	merge := " 2>&1"
	if a.Collect {
		merge = "" // a collect run prints an archive: stderr stays on the ssh stderr channel, since in stdout it would corrupt it
	}
	args := []string{"bash", "-c", prelude(a, op) + `cd -- "$1" || exit 254; shift; "$@"` + merge + ` | { head -c 8388609; cat >/dev/null; }; exit "${PIPESTATUS[0]}"`, "dyna-hostexec", cwd, "systemd-run", "--quiet", "--user", "--scope", "--unit=dyna-hostexec-" + string(op), "-p", fmt.Sprintf("RuntimeMaxSec=%g", float64(a.Timeout)/1000), "-p", "TimeoutStopSec=5", "--"}
	return append(args, a.Argv...)
}
