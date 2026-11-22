// Package browser implements the shared cookie jar model, diffing, wire types,
// and one-time v1 Chromium profile migration for owner-level shared browser profiles.
package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CookieKey uniquely identifies a cookie: (name, domain, path, partitionKey).
// PartitionKey is JSON null when the cookie is not partitioned.
type CookieKey struct {
	Name         string          `json:"name"`
	Domain       string          `json:"domain"`
	Path         string          `json:"path"`
	PartitionKey json.RawMessage `json:"partitionKey"`
}

// KeyID is a comparable map key derived from a normalized CookieKey.
type KeyID struct {
	Name         string
	Domain       string
	Path         string
	PartitionKey string
}

// HasPartitionKey reports whether the cookie is partitioned.
func (k CookieKey) HasPartitionKey() bool {
	return len(k.PartitionKey) > 0 && string(k.PartitionKey) != "null"
}

// ID returns the comparable map key for k.
func (k CookieKey) ID() KeyID {
	pk := ""
	if k.HasPartitionKey() {
		pk = string(k.PartitionKey)
	}
	return KeyID{
		Name:         k.Name,
		Domain:       k.Domain,
		Path:         k.Path,
		PartitionKey: pk,
	}
}

func compareKeyID(a, b KeyID) int {
	if c := strings.Compare(a.Domain, b.Domain); c != 0 {
		return c
	}
	if c := strings.Compare(a.Path, b.Path); c != 0 {
		return c
	}
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return strings.Compare(a.PartitionKey, b.PartitionKey)
}

// NormalizeKey validates and canonicalizes a CookieKey without including secret values in errors.
func NormalizeKey(k CookieKey) (CookieKey, error) {
	if k.Name == "" {
		return CookieKey{}, errors.New("cookie key missing name")
	}
	if k.Domain == "" {
		return CookieKey{}, errors.New("cookie key missing domain")
	}
	trimmed := bytes.TrimSpace(k.PartitionKey)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		k.PartitionKey = json.RawMessage("null")
		return k, nil
	}
	canon, err := canonicalJSON(trimmed)
	if err != nil {
		return CookieKey{}, errors.New("cookie key has invalid partitionKey")
	}
	if len(canon) == 0 || (canon[0] != '"' && canon[0] != '{') {
		return CookieKey{}, errors.New("cookie key partitionKey must be null, string, or object")
	}
	k.PartitionKey = json.RawMessage(canon)
	return k, nil
}

// Cookie wraps the exact JSON object returned by Chromium's Storage.getCookies
// alongside its validated key and expiry metadata.
type Cookie struct {
	Key     CookieKey
	Expires float64
	Session bool
	Raw     json.RawMessage
	canon   string
}

// ParseCookie validates one raw CDP cookie JSON object and returns a Cookie.
// Errors never include cookie values.
func ParseCookie(raw []byte) (Cookie, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Cookie{}, errors.New("cookie must be a JSON object")
	}
	var meta struct {
		Name         string          `json:"name"`
		Domain       string          `json:"domain"`
		Path         string          `json:"path"`
		PartitionKey json.RawMessage `json:"partitionKey"`
		Expires      float64         `json:"expires"`
		Session      bool            `json:"session"`
	}
	if err := json.Unmarshal(trimmed, &meta); err != nil {
		return Cookie{}, errors.New("invalid cookie JSON")
	}
	key, err := NormalizeKey(CookieKey{
		Name:         meta.Name,
		Domain:       meta.Domain,
		Path:         meta.Path,
		PartitionKey: meta.PartitionKey,
	})
	if err != nil {
		return Cookie{}, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return Cookie{}, errors.New("invalid cookie JSON")
	}
	canon, err := canonicalJSON(compact.Bytes())
	if err != nil {
		return Cookie{}, errors.New("invalid cookie JSON")
	}
	return Cookie{
		Key:     key,
		Expires: meta.Expires,
		Session: meta.Session,
		Raw:     append(json.RawMessage(nil), compact.Bytes()...),
		canon:   canon,
	}, nil
}

// MarshalJSON emits the exact CDP cookie JSON object.
func (c Cookie) MarshalJSON() ([]byte, error) {
	if len(c.Raw) == 0 {
		return nil, errors.New("empty cookie")
	}
	return c.Raw, nil
}

// UnmarshalJSON parses and validates a CDP cookie JSON object.
func (c *Cookie) UnmarshalJSON(data []byte) error {
	parsed, err := ParseCookie(data)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// ExpiredAt reports whether the cookie is expired at now.
// Session cookies (Session == true or Expires <= 0) do not expire by timestamp.
func (c Cookie) ExpiredAt(now time.Time) bool {
	if c.Session || c.Expires <= 0 {
		return false
	}
	return c.Expires <= float64(now.UnixNano())/1e9
}

// Equal reports whether two cookies have identical canonical fields.
func (c Cookie) Equal(other Cookie) bool {
	return c.canon == other.canon
}

// CookieMap indexes cookies by their comparable KeyID.
func CookieMap(cookies []Cookie) map[KeyID]Cookie {
	m := make(map[KeyID]Cookie, len(cookies))
	for _, c := range cookies {
		m[c.Key.ID()] = c
	}
	return m
}

// DiffCookies compares current Chromium cookies against baseline at time now.
// It returns added or changed non-expired cookies as upserts, and keys missing from
// current that have not expired locally as deletes.
func DiffCookies(baseline map[KeyID]Cookie, current []Cookie, now time.Time) (upserts []Cookie, deletes []CookieKey) {
	curMap := CookieMap(current)
	for id, cur := range curMap {
		if cur.ExpiredAt(now) {
			continue
		}
		prev, existed := baseline[id]
		if !existed || !prev.Equal(cur) {
			upserts = append(upserts, cur)
		}
	}
	for id, prev := range baseline {
		if _, stillPresent := curMap[id]; stillPresent {
			continue
		}
		if prev.ExpiredAt(now) {
			continue
		}
		deletes = append(deletes, prev.Key)
	}
	return upserts, deletes
}

func canonicalJSON(data []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("decode JSON: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode JSON: %w", err)
	}
	return string(out), nil
}
