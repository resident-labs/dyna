package harness

import (
	"regexp"
)

const masked = "[masked]"

// shapes are tokens that are secret wherever they appear.
var shapes = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}|\bsk-[A-Za-z0-9_-]{20,}|\bxox[bp]-[A-Za-z0-9-]{10,}|\b(?:AKIA|ASIA)[0-9A-Z]{16}\b|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// assignments are `NAME=value` or `"name": "value"` where the name ends in a secret word.
var assignments = regexp.MustCompile(`(?i)([A-Za-z0-9_.-]*(?:token|secret|password|passwd|_key|\bkey)["']?\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;]+)`)

// credential is a run of at least 12 token characters.
var credential = regexp.MustCompile(`^[A-Za-z0-9_\-+/=.~]{12,}$`)

// identifier is a bare name with no digit, such as `some_long_name`: code, not a credential.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z_.]*$`)

// Mask hides secrets in command output. The pattern list is fixed. An assignment is masked only when its value
// looks like a credential, so `key: &T` and `let key = (x, y)` stay as written.
func Mask(text string) string {
	return assignments.ReplaceAllStringFunc(shapes.ReplaceAllString(text, masked), func(match string) string {
		parts := assignments.FindStringSubmatch(match)
		name, value := parts[1], parts[2]
		quoted := value[0] == '"' || value[0] == '\''
		if quoted {
			value = value[1 : len(value)-1]
		}
		if !credential.MatchString(value) || (!quoted && identifier.MatchString(value)) {
			return match
		}
		return name + masked
	})
}
