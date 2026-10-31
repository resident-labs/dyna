package connect

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

type proofHeader struct {
	Alg string          `json:"alg"`
	Typ string          `json:"typ"`
	JWK json.RawMessage `json:"jwk"`
}

// ecJWK is a browser's P-256 public key.
type ecJWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// thumbprint is RFC 7638: SHA-256 of the required members in order.
func (k ecJWK) thumbprint() string {
	s := `{"crv":"` + k.Crv + `","kty":"` + k.Kty + `","x":"` + k.X + `","y":"` + k.Y + `"}`
	return hashToken(s)
}

func (k ecJWK) public() (*ecdsa.PublicKey, error) {
	if k.Kty != "EC" || k.Crv != "P-256" {
		return nil, errors.New("the key must be EC P-256")
	}
	x, err1 := b64.DecodeString(k.X)
	y, err2 := b64.DecodeString(k.Y)
	if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
		return nil, errors.New("bad key coordinates")
	}
	raw := append([]byte{4}, append(x, y...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		return nil, errors.New("the key is not on P-256")
	}
	return pub, nil
}

type proofClaims struct {
	Htm string `json:"htm"`
	Htu string `json:"htu"`
	Iat int64  `json:"iat"`
	Jti string `json:"jti"`
}

// Proof is a checked browser key proof.
type Proof struct {
	JKT string
	JTI string
}

// verifyProof checks a DPoP-style proof: an ES256 JWS whose header carries
// the browser's public key, for this method and URL, made in the last
// minute. The caller checks the thumbprint and that the jti is new.
func verifyProof(tok, htu string, now time.Time) (Proof, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || len(tok) > 8192 {
		return Proof{}, errors.New("invalid proof")
	}
	headerBytes, e1 := b64.DecodeString(parts[0])
	payload, e2 := b64.DecodeString(parts[1])
	sig, e3 := b64.DecodeString(parts[2])
	var header proofHeader
	if e1 != nil || e2 != nil || e3 != nil || json.Unmarshal(headerBytes, &header) != nil {
		return Proof{}, errors.New("invalid proof")
	}
	if header.Alg != "ES256" || header.Typ != "dpop+jwt" || len(header.JWK) == 0 {
		return Proof{}, errors.New("the proof must be an ES256 dpop+jwt with its key")
	}
	var k ecJWK
	if err := json.Unmarshal(header.JWK, &k); err != nil {
		return Proof{}, errors.New("bad proof key")
	}
	pub, err := k.public()
	if err != nil {
		return Proof{}, err
	}
	if len(sig) != 64 {
		return Proof{}, errors.New("bad proof signature")
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, d[:], r, s) {
		return Proof{}, errors.New("the proof signature does not match its key")
	}
	var c proofClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Proof{}, errors.New("bad proof claims")
	}
	iat := time.Unix(c.Iat, 0)
	if c.Htm != "POST" || c.Htu != htu {
		return Proof{}, fmt.Errorf("the proof is for %s %s, not this request", c.Htm, c.Htu)
	}
	if d := now.Sub(iat); d > time.Minute || d < -time.Minute {
		return Proof{}, errors.New("the proof is too old or from the future; check the device clock")
	}
	if len(c.Jti) < 16 || len(c.Jti) > 64 {
		return Proof{}, errors.New("bad proof jti")
	}
	return Proof{JKT: k.thumbprint(), JTI: c.Jti}, nil
}
