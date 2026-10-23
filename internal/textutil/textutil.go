// Package textutil bounds and flattens text for one-line previews, labels and
// notices. Rune bounds count runes, so a cut never splits a UTF-8 encoding.
package textutil

import "strings"

// Ellipsis marks text that a rune bound cut.
const Ellipsis = "…"

// OneLine collapses every run of white space, newlines included, to one space
// and trims both ends.
func OneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Clip returns at most n runes of s, marking a cut with an ellipsis. A string
// that already fits comes back unchanged, so most callers need no length check.
func Clip(s string, n int) string {
	if n <= 0 {
		return Ellipsis
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + Ellipsis
}

// ClipOneLine collapses s to one line and then bounds it to n runes, for a
// preview that must not contain a newline.
func ClipOneLine(s string, n int) string {
	return Clip(OneLine(s), n)
}

// FirstLine returns the first line of s that has text, trimmed. It returns ""
// when every line is blank.
func FirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Prefix returns at most n bytes of s, cut back to the start of the last whole
// rune, without a mark. Use it when the bound is a byte budget, such as a
// packet or a call argument; use Clip when the bound counts characters.
func Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	for n > 0 && (s[n]&0xc0) == 0x80 {
		n--
	}
	return s[:n]
}
