// Package shellquote quotes strings for POSIX shell command lines.
package shellquote

import "strings"

// Quote wraps s in single quotes so a POSIX shell reads it as one word.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
