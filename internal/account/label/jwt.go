package label

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// jwtClaims reads the claims of a JWT-shaped token without verifying it. The
// provider already issued it; Dyna reads only public fields such as exp.
func jwtClaims(token string) (map[string]json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid token")
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(raw, &claims) != nil || claims == nil {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// jsonString reads a JSON string value, or "" for anything else.
func jsonString(raw json.RawMessage) string { var s string; _ = json.Unmarshal(raw, &s); return s }
