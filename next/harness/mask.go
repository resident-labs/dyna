package harness

import "regexp"

const masked = "[masked]"

// shapes are tokens that are secret wherever they appear.
var shapes = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}|\bsk-[A-Za-z0-9_-]{20,}|\bxox[bp]-[A-Za-z0-9-]{10,}|\b(?:AKIA|ASIA)[0-9A-Z]{16}\b|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// assignments are `NAME=value` or `"name": "value"` where the name ends in a secret word.
var assignments = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|secret|password|passwd|_key|\bkey)["']?\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;]+)`)

// Mask hides secrets in command output. The pattern list is fixed.
func Mask(text string) string {
	return assignments.ReplaceAllString(shapes.ReplaceAllString(text, masked), "${1}"+masked)
}
