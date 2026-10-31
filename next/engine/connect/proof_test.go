package connect

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

func TestBrowserProof(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	jwk := ecJWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(pub[1:33]), Y: b64.EncodeToString(pub[33:])}
	header, _ := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		JWK ecJWK  `json:"jwk"`
	}{"ES256", "dpop+jwt", jwk})
	now := time.Unix(10000, 0)
	claims, _ := json.Marshal(proofClaims{Htm: "POST", Htu: "https://example.test/api/connect/session", Iat: now.Unix(), Jti: "abcdefghijklmnop"})
	input := b64.EncodeToString(header) + "." + b64.EncodeToString(claims)
	hash := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := input + "." + b64.EncodeToString(sig)
	proof, err := verifyProof(token, "https://example.test/api/connect/session", now)
	if err != nil || proof.JKT != jwk.thumbprint() {
		t.Fatalf("valid proof: %+v %v", proof, err)
	}
	if _, err := verifyProof(token, "https://other.test/api/connect/session", now); err == nil {
		t.Fatal("accepted wrong origin")
	}
	if _, err := verifyProof(token, "https://example.test/api/connect/session", now.Add(2*time.Minute)); err == nil {
		t.Fatal("accepted expired proof")
	}
}
