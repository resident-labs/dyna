package label

import (
	"encoding/base64"
	"encoding/json"
)

// testJWT builds an unsigned JWT-shaped token that carries v as its claims.
func testJWT(v map[string]string) string {
	raw, _ := json.Marshal(v)
	return "header." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}
