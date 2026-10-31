package connect

import (
	"crypto/ed25519"
	"testing"
)

func TestFingerprintVector(t *testing.T) {
	key := make(ed25519.PublicKey, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if got := Fingerprint(key); got != "DPPK-GEVG-TUWJ-5U5R" {
		t.Fatalf("fingerprint = %s", got)
	}
}
