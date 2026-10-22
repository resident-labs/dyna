package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	recordSize = 4096
	// MaxPayload is the largest plaintext Send accepts.
	MaxPayload = 3000
)

// encrypt builds an aes128gcm body (RFC 8291 and RFC 8188). The caller gives
// the ephemeral key and the salt so tests can use the RFC test vector.
func encrypt(plain []byte, uaPub *ecdh.PublicKey, auth []byte, eph *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plain) > MaxPayload {
		return nil, errors.New("webpush: payload is over 3000 bytes")
	}
	if len(salt) != 16 {
		return nil, errors.New("webpush: salt must be 16 bytes")
	}
	secret, err := eph.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := eph.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, secret, auth)
	if err != nil {
		return nil, err
	}
	keyInfo := append([]byte("WebPush: info\x00"), uaPub.Bytes()...)
	keyInfo = append(keyInfo, asPub...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	rec := append(append([]byte{}, plain...), 0x02)

	out := make([]byte, 0, 16+4+1+len(asPub)+len(rec)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return gcm.Seal(out, nonce, rec, nil), nil
}

// encryptFresh encrypts with a new ephemeral key and a new salt.
func encryptFresh(plain []byte, uaPub *ecdh.PublicKey, auth []byte) ([]byte, error) {
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(plain, uaPub, auth, eph, salt)
}
