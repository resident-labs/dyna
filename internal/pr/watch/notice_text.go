package watch

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"dyna-agent/internal/room"
)

// Bound the completed message, including its location, head, and filter summary.
// Keep the start and end of logs so their first error and final result survive.
func boundNotice(text string) string {
	text = strings.ToValidUTF8(Sanitize(text), "�")
	limit := room.MaxMessageBytes - 1
	if len(text) <= limit {
		return text
	}
	const logHeader = "\n\nLog tail:\n"
	if at := strings.Index(text, logHeader); at >= 0 {
		at += len(logHeader)
		header := cutNoticeMiddle(text[:at], limit/2)
		return header + cutNoticeMiddle(text[at:], limit-len(header))
	}
	return cutNoticeMiddle(text, limit)
}

func cutNoticeMiddle(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	// Reserve room for the marker and the decimal byte count.
	keep := limit - 80
	first := keep / 2
	for first > 0 && !utf8.RuneStart(text[first]) {
		first--
	}
	last := len(text) - (keep - first)
	for last < len(text) && !utf8.RuneStart(text[last]) {
		last++
	}
	return text[:first] + fmt.Sprintf("\n\n[... %d bytes cut from the middle ...]\n\n", last-first) + text[last:]
}
