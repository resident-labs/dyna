package connect

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

type Owner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type MachineInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Owner  Owner  `json:"owner"`
	Epoch  int64  `json:"epoch"`
	Status string `json:"status"`
	App    string `json:"app"`
	Cards  string `json:"cards"`
	Relay  string `json:"relay"`
}

type RelayClaims struct {
	Op     string `json:"op"`
	Iss    string `json:"iss"`
	Aud    string `json:"aud"`
	Sub    string `json:"sub"`
	Dev    string `json:"dev"`
	Epoch  int64  `json:"epoch"`
	JKT    string `json:"jkt"`
	Origin string `json:"origin"`
	Nonce  string `json:"nonce"`
	Iat    int64  `json:"iat"`
	Exp    int64  `json:"exp"`
}

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
}

type KeySet struct {
	Keys []JWK `json:"keys"`
}

type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	Typ string `json:"typ,omitempty"`
}

// VerifyRelay checks the signature and fixed request claims. The caller must
// atomically reject replayed nonces and compare-and-advance the durable epoch.
func VerifyRelay(token, op, relay, machine string, keys KeySet, now time.Time) (RelayClaims, error) {
	var c RelayClaims
	parts := strings.Split(token, ".")
	if len(token) > 8192 || len(parts) != 3 {
		return c, errors.New("invalid compact JWS")
	}
	header, err := b64.DecodeString(parts[0])
	if err != nil {
		return c, errors.New("invalid JWS header")
	}
	var h jwsHeader
	if json.Unmarshal(header, &h) != nil || h.Alg != "EdDSA" {
		return c, errors.New("invalid JWS algorithm")
	}
	var key ed25519.PublicKey
	for _, k := range keys.Keys {
		if k.Kid == h.Kid && k.Kty == "OKP" && k.Crv == "Ed25519" {
			b, err := b64.DecodeString(k.X)
			if err == nil && len(b) == ed25519.PublicKeySize {
				key = b
			}
			break
		}
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return c, errors.New("invalid relay signature")
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return c, errors.New("invalid relay claims")
	}
	n := now.Unix()
	if c.Op != op || c.Iss != relay || c.Aud != machine || c.Exp < n || c.Iat > n+60 || c.Exp-c.Iat > 60 || c.Exp < c.Iat || len(c.Nonce) < 16 || len(c.Nonce) > 64 {
		return c, errors.New("invalid relay claims")
	}
	for _, ch := range c.Nonce {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return c, errors.New("invalid relay nonce")
		}
	}
	return c, nil
}

func hashToken(s string) string { h := sha256.Sum256([]byte(s)); return b64.EncodeToString(h[:]) }

// Fingerprint matches the relay's machine-key fingerprint.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var b strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(h[i])%32])
	}
	return b.String()
}
