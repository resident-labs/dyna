package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeCookie(t *testing.T, obj map[string]any) Cookie {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCookie(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestJarMergeTombstonesExpiryAndVersion(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	future := float64(now.Add(24 * time.Hour).Unix())
	soon := float64(now.Add(10 * time.Minute).Unix())

	j := NewJar()

	cHost := makeCookie(t, map[string]any{
		"name":         "__Host-sid",
		"value":        "tok-host-1",
		"domain":       "app.example.com",
		"path":         "/",
		"expires":      -1,
		"size":         20,
		"httpOnly":     true,
		"secure":       true,
		"session":      true,
		"sameSite":     "Lax",
		"priority":     "High",
		"sourceScheme": "Secure",
		"sourcePort":   443,
	})
	cPart := makeCookie(t, map[string]any{
		"name":     "part",
		"value":    "tok-part-1",
		"domain":   ".embed.example.org",
		"path":     "/x",
		"expires":  future,
		"httpOnly": false,
		"secure":   true,
		"session":  false,
		"sameSite": "None",
		"partitionKey": map[string]any{
			"topLevelSite":         "https://top.example.net",
			"hasCrossSiteAncestor": false,
		},
	})
	cSoon := makeCookie(t, map[string]any{
		"name":    "short",
		"value":   "tok-short",
		"domain":  "example.com",
		"path":    "/",
		"expires": soon,
		"session": false,
	})

	// Box A pushes 3 cookies from baseVersion 0 -> version 1, no echo of own pushes.
	j, respA, _, changed, err := j.Sync(now, SyncRequest{
		BaseVersion: 0,
		Upserts:     []Cookie{cHost, cPart, cSoon},
	})
	if err != nil || !changed || respA.Version != 1 {
		t.Fatalf("sync A: ver=%d changed=%v err=%v", respA.Version, changed, err)
	}
	if len(respA.Upserts) != 0 || len(respA.Deletes) != 0 {
		t.Fatalf("sync A echoed own writes: upserts=%d deletes=%d", len(respA.Upserts), len(respA.Deletes))
	}

	// Box B initial pull (baseVersion 0) gets all 3 cookies verbatim.
	j, respB, _, _, err := j.Sync(now, SyncRequest{BaseVersion: 0})
	if err != nil || respB.Version != 1 || len(respB.Upserts) != 3 {
		t.Fatalf("pull B: ver=%d upserts=%d err=%v", respB.Version, len(respB.Upserts), err)
	}
	// Verify partitionKey is explicitly null on unpartitioned keys.
	for _, entry := range j.Cookies() {
		if entry.Key.Name == "__Host-sid" && string(entry.Key.PartitionKey) != "null" {
			t.Fatalf("unpartitioned key partitionKey = %s, want null", entry.Key.PartitionKey)
		}
		if entry.Key.Name == "part" && !entry.Key.HasPartitionKey() {
			t.Fatalf("partitioned key missing partitionKey")
		}
	}

	// Last writer wins: Box B updates __Host-sid -> v2; Box A (still at baseVersion 1) updates __Host-sid -> v3.
	cHostB := makeCookie(t, map[string]any{
		"name": "__Host-sid", "value": "tok-host-B", "domain": "app.example.com", "path": "/", "expires": -1, "session": true, "secure": true, "httpOnly": true,
	})
	cHostA := makeCookie(t, map[string]any{
		"name": "__Host-sid", "value": "tok-host-A", "domain": "app.example.com", "path": "/", "expires": -1, "session": true, "secure": true, "httpOnly": true,
	})
	j, respB2, _, _, err := j.Sync(now.Add(time.Second), SyncRequest{BaseVersion: 1, Upserts: []Cookie{cHostB}})
	if err != nil || respB2.Version != 2 {
		t.Fatalf("push B2: ver=%d err=%v", respB2.Version, err)
	}
	j, respA2, _, _, err := j.Sync(now.Add(2*time.Second), SyncRequest{BaseVersion: 1, Upserts: []Cookie{cHostA}})
	if err != nil || respA2.Version != 3 || len(respA2.Upserts) != 0 {
		t.Fatalf("push A2: ver=%d upserts=%d err=%v", respA2.Version, len(respA2.Upserts), err)
	}
	// Box B pulls since v2 and receives Box A's winning write.
	j, respB3, _, _, err := j.Sync(now.Add(3*time.Second), SyncRequest{BaseVersion: 2})
	if err != nil || respB3.Version != 3 || len(respB3.Upserts) != 1 || !respB3.Upserts[0].Equal(cHostA) {
		t.Fatalf("pull B3: ver=%d upserts=%d err=%v", respB3.Version, len(respB3.Upserts), err)
	}

	// Box A deletes partitioned cookie -> tombstone at v4; Box B pulls since v3 and receives the tombstone.
	j, respA3, _, _, err := j.Sync(now.Add(4*time.Second), SyncRequest{BaseVersion: 3, Deletes: []CookieKey{cPart.Key}})
	if err != nil || respA3.Version != 4 {
		t.Fatalf("delete A3: ver=%d err=%v", respA3.Version, err)
	}
	j, respB4, _, _, err := j.Sync(now.Add(5*time.Second), SyncRequest{BaseVersion: 3})
	if err != nil || respB4.Version != 4 || len(respB4.Deletes) != 1 || respB4.Deletes[0].ID() != cPart.Key.ID() {
		t.Fatalf("pull B4 tombstone: ver=%d deletes=%d err=%v", respB4.Version, len(respB4.Deletes), err)
	}

	// Advance time past cSoon expiry (15m later): engine drops cSoon on its own without creating a tombstone.
	afterExpiry := now.Add(15 * time.Minute)
	j, respAfterExp, _, changed, err := j.Sync(afterExpiry, SyncRequest{BaseVersion: 0})
	if err != nil || !changed {
		t.Fatalf("expiry sync: changed=%v err=%v", changed, err)
	}
	if len(respAfterExp.Upserts) != 1 || respAfterExp.Upserts[0].Key.Name != "__Host-sid" {
		t.Fatalf("expected only __Host-sid alive after expiry, got %d cookies", len(respAfterExp.Upserts))
	}
	if len(j.Tombstones()) != 1 {
		t.Fatalf("expected only 1 tombstone (not for expired cookie), got %d", len(j.Tombstones()))
	}

	// Advance time past 7-day tombstone retention: tombstone is pruned.
	after7d := now.Add(TombstoneRetention + time.Hour)
	j, resp7d, _, changed, err := j.Sync(after7d, SyncRequest{BaseVersion: 0})
	if err != nil || !changed || len(resp7d.Deletes) != 0 || len(j.Tombstones()) != 0 {
		t.Fatalf("7d prune: changed=%v deletes=%d tombstones=%d err=%v", changed, len(resp7d.Deletes), len(j.Tombstones()), err)
	}
}

func TestJarCapsRefusedWithoutMutation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()

	// Cookie count cap: 10,001 cookies refused.
	j := NewJar()
	many := make([]Cookie, MaxJarCookies+1)
	for i := range many {
		many[i] = makeCookie(t, map[string]any{
			"name": fmt.Sprintf("c%d", i), "value": "x", "domain": "example.com", "path": "/", "expires": -1, "session": true,
		})
	}
	if _, _, _, _, err := j.Sync(now, SyncRequest{Upserts: many}); !errors.Is(err, ErrJarFull) {
		t.Fatalf("over count cap err = %v, want ErrJarFull", err)
	}
	if j.Version != 0 || j.LiveCount(now) != 0 {
		t.Fatalf("jar mutated after count cap refusal: ver=%d count=%d", j.Version, j.LiveCount(now))
	}

	// Byte size cap: > 5 MiB refused.
	bigVal := strings.Repeat("a", 600*1024)
	var big []Cookie
	for i := 0; i < 10; i++ {
		big = append(big, makeCookie(t, map[string]any{
			"name": fmt.Sprintf("big%d", i), "value": bigVal, "domain": "example.com", "path": "/", "expires": -1, "session": true,
		}))
	}
	if _, _, _, _, err := j.Sync(now, SyncRequest{Upserts: big}); !errors.Is(err, ErrJarFull) {
		t.Fatalf("over byte cap err = %v, want ErrJarFull", err)
	}
	if j.Version != 0 || j.LiveCount(now) != 0 {
		t.Fatalf("jar mutated after byte cap refusal: ver=%d count=%d", j.Version, j.LiveCount(now))
	}
}

func TestDiffCookiesSkipsLocallyExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	exp := makeCookie(t, map[string]any{
		"name": "exp", "value": "1", "domain": "example.com", "path": "/", "expires": float64(now.Add(-time.Second).Unix()), "session": false,
	})
	live := makeCookie(t, map[string]any{
		"name": "live", "value": "2", "domain": "example.com", "path": "/", "expires": float64(now.Add(time.Hour).Unix()), "session": false,
	})
	added := makeCookie(t, map[string]any{
		"name": "new", "value": "3", "domain": "example.com", "path": "/", "expires": -1, "session": true,
	})
	baseline := CookieMap([]Cookie{exp, live})

	// Both exp and live disappeared from Chromium, added appeared.
	// Only live should be emitted as a delete; exp expired locally.
	upserts, deletes := DiffCookies(baseline, []Cookie{added}, now)
	if len(upserts) != 1 || upserts[0].Key.Name != "new" {
		t.Fatalf("unexpected upserts count=%d", len(upserts))
	}
	if len(deletes) != 1 || deletes[0].Name != "live" {
		t.Fatalf("unexpected deletes: %+v", deletes)
	}
}

func TestJarDiskMode0600AndNoValueInErrors(t *testing.T) {
	const secret = "SUPER_SECRET_COOKIE_TOKEN_999"
	_, err := ParseCookie([]byte(`{"name":"sid","value":"` + secret + `","domain":"","path":"/"}`))
	if err == nil {
		t.Fatal("expected error for missing domain")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("error message leaked cookie value")
	}
	// RFC 6265 nameless cookies (empty name, non-empty domain) must be accepted.
	nameless, err := ParseCookie([]byte(`{"name":"","value":"bare","domain":"example.com","path":"/","expires":-1,"session":true}`))
	if err != nil {
		t.Fatalf("nameless cookie rejected: %v", err)
	}
	if nameless.Key.Name != "" || nameless.Key.Domain != "example.com" {
		t.Fatalf("unexpected nameless key: %+v", nameless.Key)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	c := makeCookie(t, map[string]any{
		"name": "sid", "value": secret, "domain": "example.com", "path": "/", "expires": -1, "session": true, "httpOnly": true,
	})
	_, encoded, err := NewJarFromCookies([]Cookie{c}, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "personal", JarFilename)
	if err := SaveJarBytes(path, encoded); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("jar.json mode = %o, want 0600", mode)
	}
	loaded, ok, err := LoadJar(path)
	if err != nil || !ok || loaded.Version != 1 || loaded.LiveCount(now) != 1 {
		t.Fatalf("LoadJar: ok=%v ver=%d err=%v", ok, loaded.Version, err)
	}
}

func TestV1CookieEncryptDecrypt(t *testing.T) {
	const secret = "super-secret-cookie-token-123"

	// DB version >= 24 (SHA-256 host_key prefix).
	enc24, err := EncryptV1CookieValue("app.example.com", secret, 24)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(enc24, []byte("v10")) {
		t.Fatalf("missing v10 prefix")
	}
	got24, err := DecryptV1CookieValue("app.example.com", "", enc24, 24)
	if err != nil || got24 != secret {
		t.Fatalf("decrypt v24: got=%q err=%v", got24, err)
	}
	// Wrong host_key must be rejected without leaking the secret.
	if _, err := DecryptV1CookieValue("other.example.com", "", enc24, 24); err == nil {
		t.Fatal("expected host digest mismatch error")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatal("decrypt error leaked secret value")
	}

	// Empty value at version 24.
	encEmpty, err := EncryptV1CookieValue("plain.example.com", "", 24)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecryptV1CookieValue("plain.example.com", "", encEmpty, 24); err != nil || got != "" {
		t.Fatalf("decrypt empty v24: got=%q err=%v", got, err)
	}

	// DB version < 24 (no host_key digest prefix).
	enc23, err := EncryptV1CookieValue("legacy.example.com", secret, 23)
	if err != nil {
		t.Fatal(err)
	}
	if got23, err := DecryptV1CookieValue("legacy.example.com", "", enc23, 23); err != nil || got23 != secret {
		t.Fatalf("decrypt v23: got=%q err=%v", got23, err)
	}

	// Plaintext fallback when encrypted_value is empty.
	if gotPlain, err := DecryptV1CookieValue("plain.example.com", "plain-val", nil, 24); err != nil || gotPlain != "plain-val" {
		t.Fatalf("plain fallback: got=%q err=%v", gotPlain, err)
	}

	// Unsupported v11 prefix fails without leaking bytes.
	if _, err := DecryptV1CookieValue("app.example.com", "", append([]byte("v11"), []byte(secret)...), 24); err == nil {
		t.Fatal("expected v11 error")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatal("v11 error leaked cookie bytes")
	}
}

func TestDecodeV1CookiesAndMigrateProfile(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	futureUnix := now.Add(48 * time.Hour).Unix()
	futureChromium := (futureUnix + chromiumEpochOffsetSec) * 1_000_000

	encHost, err := EncryptV1CookieValue("mail.example.com", "host-secret", 24)
	if err != nil {
		t.Fatal(err)
	}
	encPart, err := EncryptV1CookieValue(".shop.example.org", "part-secret", 24)
	if err != nil {
		t.Fatal(err)
	}
	encSess, err := EncryptV1CookieValue("plain.example.com", "sess-secret", 24)
	if err != nil {
		t.Fatal(err)
	}
	encNameless, err := EncryptV1CookieValue("bare.example.com", "bare-val", 24)
	if err != nil {
		t.Fatal(err)
	}

	v1db := V1CookieDB{
		Version: 24,
		Rows: []V1CookieRow{
			{
				HostKey: "mail.example.com", Name: "__Host-login", EncryptedValue: encHost, Path: "/",
				ExpiresUTC: futureChromium, IsSecure: true, IsHTTPOnly: true, IsPersistent: true, HasExpires: true,
				Priority: 1, SameSite: 1, SourceScheme: 2, SourcePort: 443, HasCrossSiteAncestor: true, // unpartitioned ignores HasCrossSiteAncestor
			},
			{
				HostKey: ".shop.example.org", TopFrameSiteKey: "https://example.net", Name: "part_login", EncryptedValue: encPart, Path: "/p",
				ExpiresUTC: futureChromium, IsSecure: true, IsHTTPOnly: false, IsPersistent: true, HasExpires: true,
				Priority: 2, SameSite: 0, SourceScheme: 2, SourcePort: 443, HasCrossSiteAncestor: false,
			},
			{
				HostKey: "plain.example.com", Name: "sess", EncryptedValue: encSess, Path: "/",
				ExpiresUTC: 0, IsSecure: false, IsHTTPOnly: true, IsPersistent: false, HasExpires: false,
				Priority: 0, SameSite: -1, SourceScheme: 1, SourcePort: 80,
			},
			{
				// RFC 6265 nameless cookie must decode cleanly.
				HostKey: "bare.example.com", Name: "", EncryptedValue: encNameless, Path: "/",
				ExpiresUTC: futureChromium, IsSecure: false, IsHTTPOnly: false, IsPersistent: true, HasExpires: true,
				Priority: 1, SameSite: -1, SourceScheme: 1, SourcePort: 80,
			},
			{
				// One undecryptable row must be skipped without aborting migration of the valid cookies.
				HostKey: "broken.example.com", Name: "bad", EncryptedValue: []byte("v11corrupt"), Path: "/",
				ExpiresUTC: futureChromium, IsPersistent: true, HasExpires: true, SameSite: -1,
			},
		},
	}

	// If every row in a non-empty DB fails to decode, DecodeV1Cookies must return an error so migration
	// preserves the v1 profile intact instead of deleting it.
	if _, err := DecodeV1Cookies(V1CookieDB{
		Version: 24,
		Rows: []V1CookieRow{{
			HostKey: "only.example.com", Name: "k", EncryptedValue: []byte("v11corrupt"), Path: "/",
		}},
	}); err == nil {
		t.Fatal("expected error when all v1 cookie rows fail to decode")
	}

	root := t.TempDir()
	profileDir := filepath.Join(root, "personal")
	prevDir := filepath.Join(root, "personal.prev")
	if err := os.MkdirAll(filepath.Join(profileDir, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "Default", "Cookies"), []byte("sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prevDir, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prevDir, "Default", "Cookies"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	// Failed reader preserves v1 files intact.
	if _, migrated, err := MigrateV1Profile(context.Background(), profileDir, prevDir, now, func(context.Context, string) (V1CookieDB, error) {
		return V1CookieDB{}, errors.New("read failed")
	}); err == nil || migrated {
		t.Fatalf("failed migration: migrated=%v err=%v", migrated, err)
	}
	if !HasV1Profile(profileDir) {
		t.Fatal("failed migration deleted v1 profile")
	}

	// Successful migration writes jar.json (0600) and deletes v1 Default/ and .prev.
	jar, migrated, err := MigrateV1Profile(context.Background(), profileDir, prevDir, now, func(context.Context, string) (V1CookieDB, error) {
		return v1db, nil
	})
	if err != nil || !migrated {
		t.Fatalf("MigrateV1Profile: migrated=%v err=%v", migrated, err)
	}
	if jar.Version != 1 || jar.LiveCount(now) != 4 {
		t.Fatalf("migrated jar: ver=%d count=%d, want ver=1 count=4", jar.Version, jar.LiveCount(now))
	}

	byName := map[string]map[string]any{}
	for _, entry := range jar.Cookies() {
		var m map[string]any
		if err := json.Unmarshal(entry.Cookie.Raw, &m); err != nil {
			t.Fatal(err)
		}
		byName[entry.Key.Name] = m
	}
	if byName["__Host-login"]["value"] != "host-secret" || byName["__Host-login"]["sameSite"] != "Lax" || byName["__Host-login"]["partitionKey"] != nil {
		t.Fatalf("unexpected __Host-login: %v", byName["__Host-login"])
	}
	if byName["part_login"]["value"] != "part-secret" || byName["part_login"]["sameSite"] != "None" {
		t.Fatalf("unexpected part_login: %v", byName["part_login"])
	}
	pk, _ := byName["part_login"]["partitionKey"].(map[string]any)
	if pk == nil || pk["topLevelSite"] != "https://example.net" || pk["hasCrossSiteAncestor"] != false {
		t.Fatalf("unexpected part_login partitionKey: %v", byName["part_login"]["partitionKey"])
	}
	if byName["sess"]["value"] != "sess-secret" || byName["sess"]["session"] != true || byName["sess"]["expires"] != float64(-1) || byName["sess"]["sameSite"] != nil {
		t.Fatalf("unexpected sess: %v", byName["sess"])
	}
	if byName[""]["value"] != "bare-val" || byName[""]["domain"] != "bare.example.com" {
		t.Fatalf("unexpected nameless cookie: %v", byName[""])
	}

	jarPath := filepath.Join(profileDir, JarFilename)
	info, err := os.Stat(jarPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("jar.json stat: err=%v mode=%v", err, info)
	}
	entries, err := os.ReadDir(profileDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != JarFilename {
		t.Fatalf("profileDir after migration should only contain jar.json, got %d entries (err=%v)", len(entries), err)
	}
	if _, err := os.Stat(prevDir); !os.IsNotExist(err) {
		t.Fatalf(".prev not deleted: %v", err)
	}

	// Second call is a no-op.
	if _, migratedAgain, err := MigrateV1Profile(context.Background(), profileDir, prevDir, now, nil); err != nil || migratedAgain {
		t.Fatalf("second MigrateV1Profile: migrated=%v err=%v", migratedAgain, err)
	}
}
