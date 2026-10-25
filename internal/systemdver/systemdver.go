// Package systemdver reads the systemd version that decides one scope argument:
// the one that stops systemd from expanding environment variables in the command
// and arguments it runs. systemd 254 added that argument, and an older systemd
// rejects it, so a scope on an older machine runs without it.
package systemdver

import (
	"strconv"
	"strings"
)

// NoExpandFlag stops systemd-run from expanding environment variables in a
// command's arguments, which would rewrite a script or a command line that holds
// $ signs of its own.
const NoExpandFlag = "--expand-environment=no"

// NoExpandVersion is the first systemd that knows NoExpandFlag. Version 253 and
// earlier have no such option and expand nothing themselves, so they run a scope
// as written without it.
const NoExpandVersion = 254

// NeedsNoExpand reports whether a systemd of this major version knows
// NoExpandFlag.
func NeedsNoExpand(major int) bool { return major >= NoExpandVersion }

// Major reads the major version from `systemd-run --version`, which starts with
// "systemd 252 (252.39-1~deb12u1)".
func Major(version string) (int, bool) {
	fields := strings.Fields(version)
	if len(fields) < 2 || fields[0] != "systemd" {
		return 0, false
	}
	major, err := strconv.Atoi(strings.SplitN(fields[1], ".", 2)[0])
	if err != nil {
		return 0, false
	}
	return major, true
}
