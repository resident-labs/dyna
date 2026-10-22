// Package handoff builds the bounded review paragraph shared by session handoffs.
package handoff

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Review names a source and its open work without embedding its evidence.
// Callers build references from stored IDs and supported commands, never source text.
// An empty session identifies an external root, which has no Dyna session history.
func Review(session, summary string, references ...string) string {
	source := "The external root"
	if session != "" {
		source = "Source session " + brief(session, 64)
	}
	return fmt.Sprintf("%s. Open work: %s. Review this source before acting. Read its saved handoff, history and output through these references: %s. Inspect its task directory before editing. Preserve its work.\n",
		source, brief(summary, 240), strings.Join(references, "; "))
}

func brief(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	for !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}
