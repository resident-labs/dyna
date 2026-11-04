package hostexec

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"strconv"
	"strings"
)

const OutputLimit = 8 << 20

// CollectTimeout bounds reading back a run's $DYNA_OUT.
const CollectTimeout = contract.DurationMS(2 * 60 * 1000)

// desktopVars are what every host's run takes from the user's graphical session, so GUI commands work over SSH.
const desktopVars = "WAYLAND_DISPLAY=*|HYPRLAND_INSTANCE_SIGNATURE=*|DBUS_SESSION_BUS_ADDRESS=*|XDG_RUNTIME_DIR=*|DISPLAY=*"

// podmanGuard is the shim a run puts first on its PATH (spec 6.3).
//
//go:embed podman-guard.sh
var podmanGuard []byte

// OutDir is where a run's $DYNA_OUT lives on the host.
func OutDir(op contract.OpID) string { return `${TMPDIR:-/tmp}/dyna-out-` + string(op) }

// prelude makes a run's $DYNA_OUT and imports the session environment, if the user has one. A collect run has neither.
func prelude(a contract.HostexecRunArgs, op contract.OpID) string {
	if a.Collect {
		return ""
	}
	s := `d=` + OutDir(op) + `; mkdir -m 0700 -- "$d" || exit 254; export DYNA_OUT=$d; `
	s += `g=$d.bin; mkdir -m 0700 -- "$g" || exit 254; printf %s ` + base64.StdEncoding.EncodeToString(podmanGuard) + ` | base64 -d > "$g/podman"; chmod 0700 "$g/podman"; command -v docker >/dev/null && ln -s podman "$g/docker"; export PATH=$g:$PATH; `
	s += `while IFS= read -r l; do case $l in ` + desktopVars + `) export "$l";; esac; done < <(systemctl --user show-environment 2>/dev/null); `
	return s
}

// collectScript prints a tar of the files a run left in $DYNA_OUT, or nothing when there are none, and removes the folder.
func collectScript(op contract.OpID) string {
	d := OutDir(op)
	return `d=` + d + `; rc=0; if [ -n "$(find "$d" -type f -print -quit 2>/dev/null)" ]; then tar -C "$d" -cf - . || rc=3; fi; rm -rf -- "$d" "$d.bin" "$d.run" || rc=3; exit $rc`
}

// CollectArgs is the run that reads back and removes a finished run's $DYNA_OUT.
func CollectArgs(a contract.HostexecRunArgs, op contract.OpID) contract.HostexecRunArgs {
	a.Argv, a.Cwd, a.Collect, a.Timeout = []string{"bash", "-c", collectScript(op)}, "", true, CollectTimeout
	return a
}

// runWords is the only command systemd-run sees. It has no $ or %, which systemd 254 and later expand in the words after "--".
// The user's words travel quoted in DYNA_ARGV, so the command gets exactly the text given.
const runWords = "eval \"exec env -u DYNA_ARGV `printenv DYNA_ARGV`\""

// runner is what a run's detached process executes, with the unit, the timeout in seconds and the run's file prefix as its words.
// It keeps the output (merged, at most OutputLimit and a byte) and the exit status in files, so no connection is needed to keep them.
const runner = `{ systemd-run --quiet --user --scope --unit="$1" -p "RuntimeMaxSec=$2" -p TimeoutStopSec=5 -- bash -c '` + runWords + `'; } 2>&1 | { head -c 8388609 >"$3/log"; cat >/dev/null; }; c=${PIPESTATUS[0]}; (umask 077; echo "$c" >"$3/exit.tmp") && mv -- "$3/exit.tmp" "$3/exit"`

// follow prints a run's output from its start, until its runner ends, and exits with its status. It needs r, the run's folder.
const follow = `tail -c +1 -s 0.2 --pid="$(cat "$r/pid")" -f -- "$r/log"; [ -f "$r/exit" ] || { echo '[the runner ended on the host without an exit status]'; exit 255; }; exit "$(cat "$r/exit")"`

func RunArgs(a contract.HostexecRunArgs, op contract.OpID) []string {
	cwd := a.Cwd
	if cwd == "" {
		cwd = "."
	}
	var script string
	switch {
	case a.Attach:
		script = `r=` + OutDir(op) + `.run; [ -f "$r/pid" ] || { echo '` + strings.TrimSuffix(NoRunOutput(op), "\n") + `'; exit ` + strconv.Itoa(NoRunStatus) + `; }; ` + follow
	case a.Collect:
		// A collect run prints an archive: its stderr stays on the ssh stderr channel, since in stdout it would corrupt it.
		script = `cd -- "$1" || exit 254; u=$2; t=$3; shift 3; DYNA_ARGV=$(printf '%q ' "$@") systemd-run --quiet --user --scope --unit="$u" -p "RuntimeMaxSec=$t" -p TimeoutStopSec=5 -- bash -c '` + runWords + `' | { head -c 8388609; cat >/dev/null; }; exit "${PIPESTATUS[0]}"`
	default:
		// The prelude and the cd sit inside the merge, so a failed mkdir or a missing cwd shows in the output like the command's own errors.
		// The runner is detached (setsid, in a subshell so that its end is reaped and tail's --pid sees it), which is why the call can go away.
		script = `{ ` + prelude(a, op) + `cd -- "$1" || exit 254; u=$2; t=$3; shift 3; export DYNA_ARGV=$(printf '%q ' "$@"); r=$d.run; mkdir -m 0700 -- "$r" || exit 254; (umask 077; : >"$r/log"); ( setsid bash -c "$(printf %s ` + base64.StdEncoding.EncodeToString([]byte(runner)) + ` | base64 -d)" dyna-runner "$u" "$t" "$r" </dev/null >/dev/null 2>&1 & (umask 077; echo $! >"$r/pid") ); } 2>&1; ` + follow
	}
	args := []string{"bash", "-c", script, "dyna-hostexec", cwd, "dyna-hostexec-" + string(op), fmt.Sprintf("%g", float64(a.Timeout)/1000)}
	return append(args, a.Argv...)
}

// NoRunStatus is the exit status of a call that follows a run the host holds no files for. A command that exits with it at
// that moment reads the same.
const NoRunStatus = 253

// NoRunOutput is what a call prints that follows a run the host holds no files for. With NoRunStatus it says the files are gone;
// the status alone could be a command's own.
func NoRunOutput(op contract.OpID) string { return "[no run files for " + string(op) + "]\n" }

// MaxQuoted is the most a run's quoted words may take as the value of DYNA_ARGV: one environment string is at most 128 KiB with its name and end byte.
const MaxQuoted = 128<<10 - len("DYNA_ARGV=") - 1

// QuotedLen is how many bytes the words take once quoted by bash's printf %q, each followed by a space. A byte outside ASCII counts as an octal escape, so the size is an upper bound there.
func QuotedLen(argv []string) int {
	n := 0
	for _, w := range argv {
		n += 1 + len(w)
		if w == "" {
			n += 2
			continue
		}
		ansi := false
		for i := 0; i < len(w); i++ {
			if c := w[i]; c < 32 || c >= 127 {
				ansi = true
			}
		}
		if ansi {
			n += 3
			for i := 0; i < len(w); i++ {
				switch c := w[i]; {
				case c == '\\' || c == '\'' || c == 7 || c == 8 || c == 9 || c == 10 || c == 11 || c == 12 || c == 13 || c == 27:
					n++
				case c < 32 || c >= 127:
					n += 3
				}
			}
			continue
		}
		for i := 0; i < len(w); i++ {
			switch c := w[i]; {
			case c >= '0' && c <= '9', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_', c == '/', c == '.', c == '-', c == '+', c == '=', c == ':', c == '@', c == '%':
			case (c == '#' || c == '~') && i > 0:
			default:
				n++
			}
		}
	}
	return n
}
