// Package webpush sends Web Push messages from the Dyna machine straight to
// the browser's push service. No relay is involved.
//
// Rules this package keeps:
//
//   - VAPID (RFC 8292). The machine has one P-256 key, made once and kept in
//     a mode 0600 file in the caller's directory. Each request carries
//     "Authorization: vapid t=<JWT>, k=<public key>". The JWT is ES256 with
//     aud = scheme and host of the endpoint, exp = now + 12 hours and
//     sub = a contact URL.
//   - Encryption (RFC 8291) with content coding aes128gcm (RFC 8188): one
//     record of 4096 bytes, a fresh ephemeral key and a fresh 16-byte salt
//     for every message. The plaintext is at most 3000 bytes.
//   - Endpoints are allowlisted: https, no userinfo, default port, and a host
//     that is fcm.googleapis.com, jmt<N>.google.com, web.push.apple.com, *.push.services.mozilla.com
//     or *.notify.windows.com. The check runs at subscribe time and again at
//     send time. The HTTP client follows no redirects and times out after
//     15 seconds.
//   - One subscription per device ID. The last subscribe wins. A 404 or 410
//     from the push service deletes the subscription. A 429 or 5xx is
//     returned as an error; this package does not retry.
//   - Endpoints are capability URLs. Nothing here logs an endpoint path, a
//     key or a payload, and errors name the endpoint host only.
package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/fsutil"
)

const (
	// DefaultContact is the VAPID sub claim when the caller gives none.
	DefaultContact = "https://connect.residentlabs.dev"
	vapidLife      = 12 * time.Hour
	keyFile        = "vapid.pem"
)

var b64 = base64.RawURLEncoding

// decodeB64 reads base64url with or without trailing padding.
func decodeB64(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// VAPID holds the machine's application server key.
type VAPID struct {
	key     *ecdsa.PrivateKey
	contact string
	now     func() time.Time
}

// LoadOrCreateVAPID reads the key from dir/vapid.pem, or makes it (mode 0600)
// when the file is missing. The caller makes dir, mode 0700. An empty contact
// means DefaultContact.
func LoadOrCreateVAPID(dir, contact string) (*VAPID, error) {
	if contact == "" {
		contact = DefaultContact
	}
	v := &VAPID{contact: contact, now: time.Now}
	path := filepath.Join(dir, keyFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		blk, _ := pem.Decode(data)
		if blk == nil {
			return nil, errors.New("webpush: vapid key file is not PEM")
		}
		k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("webpush: read vapid key: %w", err)
		}
		ek, ok := k.(*ecdsa.PrivateKey)
		if !ok || ek.Curve != elliptic.P256() {
			return nil, errors.New("webpush: vapid key is not P-256")
		}
		v.key = ek
	case errors.Is(err, os.ErrNotExist):
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		if err := writeFile0600(path, pemBytes); err != nil {
			return nil, err
		}
		v.key = k
	default:
		return nil, err
	}
	return v, nil
}

// PublicKey is the base64url uncompressed point (65 bytes). The browser uses
// it as applicationServerKey.
func (v *VAPID) PublicKey() string {
	pub, err := v.key.PublicKey.Bytes()
	if err != nil {
		return ""
	}
	return b64.EncodeToString(pub)
}

// Authorization builds the Authorization header value for an endpoint.
func (v *VAPID) Authorization(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("webpush: bad endpoint")
	}
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": v.now().Add(vapidLife).Unix(),
		"sub": v.contact,
	})
	if err != nil {
		return "", err
	}
	signing := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	der, err := ecdsa.SignASN1(rand.Reader, v.key, sum[:])
	if err != nil {
		return "", err
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := make([]byte, 64)
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(raw) + ", k=" + v.PublicKey(), nil
}

// writeFile0600 writes a file by temp file and rename.
func writeFile0600(path string, data []byte) error {
	return fsutil.WriteFile(path, data, 0o600, false)
}
