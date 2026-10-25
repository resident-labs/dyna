package native

import (
	"bytes"
	"encoding/json"
	"strings"

	"dyna-agent/internal/account/store"
)

// CredentialSecretValues returns the secret string values of a saved
// credential, so a caller can reject a usage result that copies one. The values
// never leave the caller's process and are never logged here.
func CredentialSecretValues(c store.Credential) []string {
	if len(c.Payload) == 0 || len(c.Payload) > 4<<20 {
		return nil
	}
	var value any
	if json.Unmarshal(c.Payload, &value) != nil {
		// A credential that is not JSON is the secret itself.
		if len(c.Payload) >= 8 && !bytes.ContainsAny(c.Payload, " \t\r\n") {
			return []string{string(c.Payload)}
		}
		return nil
	}
	var out []string
	collectSecretValues(value, &out)
	return out
}

func collectSecretValues(value any, out *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok && isSecretKey(key) {
				if len(text) >= 8 {
					*out = append(*out, text)
				}
				continue
			}
			collectSecretValues(child, out)
		}
	case []any:
		for _, child := range typed {
			collectSecretValues(child, out)
		}
	}
}

// isSecretKey names the JSON fields that hold a credential value. It matches
// the field names of every saved harness credential.
func isSecretKey(key string) bool {
	switch strings.ToLower(key) {
	case "key", "api_key", "apikey", "access", "access_token", "accesstoken",
		"refresh", "refresh_token", "refreshtoken", "token", "auth_token",
		"authtoken", "id_token", "idtoken", "secret", "password":
		return true
	}
	return false
}
