// This file supplies Go build defaults for agent and check processes.
package childenv

import (
	"os"
	"strings"
)

// Trimpath preserves flags and adds -trimpath unless it is already specified.
// An explicit -trimpath=false takes precedence over the default.
func Trimpath(flags string) string {
	// GOFLAGS permits quotes around whole fields, without shell unescaping.
	remaining := flags
	for {
		remaining = strings.TrimLeft(remaining, " \t\r\n")
		if remaining == "" {
			break
		}
		var field string
		if remaining[0] == '\'' || remaining[0] == '"' {
			end := strings.IndexByte(remaining[1:], remaining[0])
			if end < 0 {
				// Leave invalid GOFLAGS intact for Go to diagnose.
				return flags
			}
			field, remaining = remaining[1:end+1], remaining[end+2:]
		} else {
			end := strings.IndexAny(remaining, " \t\r\n")
			if end < 0 {
				field, remaining = remaining, ""
			} else {
				field, remaining = remaining[:end], remaining[end:]
			}
		}
		// Go also accepts a double dash before a GOFLAGS flag name.
		field = strings.TrimPrefix(field, "-")
		field = strings.TrimPrefix(field, "-")
		if field == "trimpath" || strings.HasPrefix(field, "trimpath=") {
			return flags
		}
	}
	if flags == "" {
		return "-trimpath"
	}
	return flags + " -trimpath"
}

// SetTrimpath sets GOFLAGS in a private profile environment map. Profile values,
// including an explicit empty value, override the inherited environment.
func SetTrimpath(env map[string]string) {
	flags, ok := env["GOFLAGS"]
	if !ok {
		flags = os.Getenv("GOFLAGS")
	}
	env["GOFLAGS"] = Trimpath(flags)
}
