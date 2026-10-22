package connect

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b64.EncodeToString(b)
}

func hashToken(s string) string {
	h := sha256.Sum256([]byte(s))
	return b64.EncodeToString(h[:])
}

// codeAlphabet matches the relay's: no 0, 1, I or O.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Fingerprint is how a person compares the machine key on two screens: the
// first 16 bytes of SHA-256 of the raw key, as four groups of four. The
// relay computes the same string.
func Fingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	var b strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[int(h[i])%32])
	}
	return b.String()
}

// --- compact JWS ---

type jwsHeader struct {
	Alg string          `json:"alg"`
	Kid string          `json:"kid,omitempty"`
	Typ string          `json:"typ,omitempty"`
	JWK json.RawMessage `json:"jwk,omitempty"`
}

type jws struct {
	header  jwsHeader
	payload []byte
	input   string
	sig     []byte
}

func parseJWS(tok string) (*jws, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || len(tok) > 8192 {
		return nil, errors.New("not a compact JWS")
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("bad JWS header")
	}
	var h jwsHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, errors.New("bad JWS header")
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("bad JWS payload")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("bad JWS signature")
	}
	return &jws{header: h, payload: pb, input: parts[0] + "." + parts[1], sig: sig}, nil
}

// signMachine makes the JWS the relay checks on every machine request.
func signMachine(key ed25519.PrivateKey, claims any) (string, error) {
	h, _ := json.Marshal(jwsHeader{Alg: "EdDSA", Typ: "dyna-machine+jws"})
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	return in + "." + b64.EncodeToString(ed25519.Sign(key, []byte(in))), nil
}

// --- relay key set ---

// JWK is one relay public key.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
}

// KeySet is the relay's published keys and when this machine fetched them.
type KeySet struct {
	Keys    []JWK     `json:"keys"`
	Fetched time.Time `json:"fetched"`
}

func (ks KeySet) key(kid string) (ed25519.PublicKey, bool) {
	for _, k := range ks.Keys {
		if k.Kid == kid && k.Kty == "OKP" && k.Crv == "Ed25519" {
			b, err := b64.DecodeString(k.X)
			if err == nil && len(b) == ed25519.PublicKeySize {
				return ed25519.PublicKey(b), true
			}
		}
	}
	return nil, false
}

// --- browser key proofs ---

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
	Iat time.Time
}

// verifyProof checks a DPoP-style proof: an ES256 JWS whose header carries
// the browser's public key, for this method and URL, made in the last
// minute. The caller checks the thumbprint and that the jti is new.
func verifyProof(tok, htu string, now time.Time) (Proof, error) {
	j, err := parseJWS(tok)
	if err != nil {
		return Proof{}, err
	}
	if j.header.Alg != "ES256" || j.header.Typ != "dpop+jwt" || len(j.header.JWK) == 0 {
		return Proof{}, errors.New("the proof must be an ES256 dpop+jwt with its key")
	}
	var k ecJWK
	if err := json.Unmarshal(j.header.JWK, &k); err != nil {
		return Proof{}, errors.New("bad proof key")
	}
	pub, err := k.public()
	if err != nil {
		return Proof{}, err
	}
	if len(j.sig) != 64 {
		return Proof{}, errors.New("bad proof signature")
	}
	d := sha256.Sum256([]byte(j.input))
	r, s := new(big.Int).SetBytes(j.sig[:32]), new(big.Int).SetBytes(j.sig[32:])
	if !ecdsa.Verify(pub, d[:], r, s) {
		return Proof{}, errors.New("the proof signature does not match its key")
	}
	var c proofClaims
	if err := json.Unmarshal(j.payload, &c); err != nil {
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
	return Proof{JKT: k.thumbprint(), JTI: c.Jti, Iat: iat}, nil
}
