package roommcp

import (
	"encoding/json"
	"strings"
)

// Scrub replaces every secret in text with [redacted]. It also removes the
// JSON-escaped form, because structured results and errors are encoded JSON.
func Scrub(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[redacted]")
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}
