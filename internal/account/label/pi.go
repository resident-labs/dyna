package label

import (
	"encoding/json"
	"net/mail"
	"strings"
	"unicode"
)

// PiDefaultLabel uses only an email claim or the last four characters of a
// key. Tokens and short keys must never become account labels.
func PiDefaultLabel(provider string, raw json.RawMessage) string {
	var entry struct {
		Type   string `json:"type"`
		Key    string `json:"key"`
		Access string `json:"access"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return provider + " login"
	}
	if entry.Type == "api_key" {
		return PiKeyLabel(provider, entry.Key)
	}
	claims, err := jwtClaims(entry.Access)
	if err == nil {
		email := strings.TrimSpace(jsonString(claims["email"]))
		if address, err := mail.ParseAddress(email); err == nil && address.Address == email && len(email) <= 254 {
			return email
		}
	}
	return provider + " login"
}

// PiKeyLabel never reveals a whole short key.
func PiKeyLabel(provider, key string) string {
	runes := []rune(key)
	if len(runes) > 4 {
		suffix := runes[len(runes)-4:]
		safe := true
		for _, r := range suffix {
			if !unicode.IsPrint(r) || unicode.IsSpace(r) {
				safe = false
			}
		}
		if safe {
			return provider + " · " + string(suffix)
		}
	}
	return provider + " key"
}
