package browser

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	// JarFilename is the cookie jar file inside <data>/browsers/<name>/.
	JarFilename = "jar.json"

	// chromiumEpochOffsetSec is the number of seconds between 1601-01-01 and 1970-01-01 UTC.
	chromiumEpochOffsetSec = 11644473600
	// chromiumHostHashVersion is the Chromium cookie DB meta version that prepends SHA-256(host_key)
	// to the decrypted cookie value plaintext.
	chromiumHostHashVersion = 24
)

var (
	v1BasicKey []byte
	v1BasicIV  = bytes.Repeat([]byte{' '}, aes.BlockSize)
)

func init() {
	var err error
	v1BasicKey, err = pbkdf2.Key(sha1.New, "peanuts", []byte("saltysalt"), 1, 16)
	if err != nil {
		panic(err)
	}
}

// V1CookieRow holds raw column values read from a v1 Chromium Default/Cookies SQLite database.
type V1CookieRow struct {
	HostKey              string
	TopFrameSiteKey      string
	Name                 string
	Value                string
	EncryptedValue       []byte
	Path                 string
	ExpiresUTC           int64
	IsSecure             bool
	IsHTTPOnly           bool
	IsPersistent         bool
	HasExpires           bool
	Priority             int
	SameSite             int
	SourceScheme         int
	SourcePort           int
	HasCrossSiteAncestor bool
}

// V1CookieDB holds the meta schema version and rows read from a v1 Chromium Cookies database.
type V1CookieDB struct {
	Version int
	Rows    []V1CookieRow
}

// FindV1CookiesDB returns the path to a v1 Chromium Cookies SQLite file inside dir, if present.
func FindV1CookiesDB(dir string) (string, bool) {
	for _, rel := range []string{
		filepath.Join("Default", "Cookies"),
		filepath.Join("Default", "Network", "Cookies"),
	} {
		path := filepath.Join(dir, rel)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

// HasV1Profile reports whether dir holds a v1 Chromium profile with a Cookies database.
func HasV1Profile(dir string) bool {
	_, ok := FindV1CookiesDB(dir)
	return ok
}

// DecryptV1CookieValue decrypts a Chromium Linux --password-store=basic ("v10") encrypted cookie value.
// For DB meta version >= 24, it verifies and strips the 32-byte SHA-256(host_key) prefix.
// Errors never contain plaintext or ciphertext cookie values.
func DecryptV1CookieValue(hostKey, plain string, encrypted []byte, dbVersion int) (string, error) {
	if len(encrypted) == 0 {
		return plain, nil
	}
	if len(encrypted) < 3 {
		return "", errors.New("v1 encrypted cookie shorter than version prefix")
	}
	if !bytes.Equal(encrypted[:3], []byte("v10")) {
		return "", errors.New("unsupported v1 cookie encryption prefix (expected v10)")
	}
	ciphertext := encrypted[3:]
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("invalid v1 encrypted cookie block length")
	}
	block, err := aes.NewCipher(v1BasicKey)
	if err != nil {
		return "", errors.New("init AES cipher")
	}
	buf := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, v1BasicIV).CryptBlocks(buf, ciphertext)

	pad := int(buf[len(buf)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(buf) {
		return "", errors.New("invalid v1 cookie padding")
	}
	for i := len(buf) - pad; i < len(buf); i++ {
		if buf[i] != byte(pad) {
			return "", errors.New("invalid v1 cookie padding")
		}
	}
	out := buf[:len(buf)-pad]

	if dbVersion >= chromiumHostHashVersion {
		if len(out) < sha256.Size {
			return "", errors.New("v1 cookie plaintext missing host digest prefix")
		}
		want := sha256.Sum256([]byte(hostKey))
		if !bytes.Equal(out[:sha256.Size], want[:]) {
			return "", errors.New("v1 cookie host digest mismatch")
		}
		out = out[sha256.Size:]
	}
	return string(out), nil
}

// EncryptV1CookieValue encrypts a cookie value using Chromium's Linux --password-store=basic ("v10")
// format (AES-128-CBC with PBKDF2-SHA1("peanuts", "saltysalt", 1, 16) and 16-space IV), prepending
// SHA-256(hostKey) when dbVersion >= 24.
func EncryptV1CookieValue(hostKey, value string, dbVersion int) ([]byte, error) {
	var plaintext []byte
	if dbVersion >= chromiumHostHashVersion {
		sum := sha256.Sum256([]byte(hostKey))
		plaintext = append(plaintext, sum[:]...)
	}
	plaintext = append(plaintext, value...)

	pad := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+pad)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(pad)
	}

	block, err := aes.NewCipher(v1BasicKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 3+len(padded))
	copy(out[:3], "v10")
	cipher.NewCBCEncrypter(block, v1BasicIV).CryptBlocks(out[3:], padded)
	return out, nil
}

// DecodeV1Cookies decrypts and converts v1 Chromium SQLite rows into validated CDP cookies.
// Individual undecryptable or malformed rows are skipped (logged by count only, never values)
// as long as at least one row decodes; if every non-empty DB row fails, an error is returned
// so migration preserves the v1 profile intact.
func DecodeV1Cookies(db V1CookieDB) ([]Cookie, error) {
	cookies := make([]Cookie, 0, len(db.Rows))
	skipped := 0
	var firstErr error
	for i, row := range db.Rows {
		val, err := DecryptV1CookieValue(row.HostKey, row.Value, row.EncryptedValue, db.Version)
		if err != nil {
			skipped++
			if firstErr == nil {
				firstErr = fmt.Errorf("row %d: %w", i, err)
			}
			continue
		}
		path := row.Path
		if path == "" {
			path = "/"
		}
		obj := map[string]any{
			"name":     row.Name,
			"value":    val,
			"domain":   row.HostKey,
			"path":     path,
			"secure":   row.IsSecure,
			"httpOnly": row.IsHTTPOnly,
			"size":     len(row.Name) + len(val),
		}
		if row.IsPersistent && row.HasExpires && row.ExpiresUTC > 0 {
			obj["expires"] = float64(row.ExpiresUTC)/1e6 - chromiumEpochOffsetSec
			obj["session"] = false
		} else {
			obj["expires"] = -1
			obj["session"] = true
		}
		switch row.SameSite {
		case 0:
			obj["sameSite"] = "None"
		case 1:
			obj["sameSite"] = "Lax"
		case 2:
			obj["sameSite"] = "Strict"
		}
		switch row.Priority {
		case 0:
			obj["priority"] = "Low"
		case 1:
			obj["priority"] = "Medium"
		case 2:
			obj["priority"] = "High"
		}
		switch row.SourceScheme {
		case 0:
			obj["sourceScheme"] = "Unset"
		case 1:
			obj["sourceScheme"] = "NonSecure"
		case 2:
			obj["sourceScheme"] = "Secure"
		}
		if row.SourcePort >= 0 {
			obj["sourcePort"] = row.SourcePort
		}
		if row.TopFrameSiteKey != "" {
			obj["partitionKey"] = map[string]any{
				"topLevelSite":         row.TopFrameSiteKey,
				"hasCrossSiteAncestor": row.HasCrossSiteAncestor,
			}
		}

		raw, err := json.Marshal(obj)
		if err != nil {
			skipped++
			if firstErr == nil {
				firstErr = fmt.Errorf("row %d: encode cookie", i)
			}
			continue
		}
		c, err := ParseCookie(raw)
		if err != nil {
			skipped++
			if firstErr == nil {
				firstErr = fmt.Errorf("row %d: %w", i, err)
			}
			continue
		}
		cookies = append(cookies, c)
	}
	if skipped > 0 {
		if len(cookies) == 0 {
			return nil, fmt.Errorf("all %d v1 cookie rows failed to decode (%w)", skipped, firstErr)
		}
		log.Printf("browser migration: skipped %d of %d undecodable v1 cookie rows", skipped, len(db.Rows))
	}
	return cookies, nil
}

// MigrateV1Profile checks whether dir holds a v1 Chromium profile (Default/Cookies) without
// a jar.json yet. When found, it reads the SQLite rows through readDB, decrypts and converts
// them into a jar, writes jar.json atomically (0600), and removes the v1 profile files and prevDir.
// On any error, v1 files are left untouched.
func MigrateV1Profile(ctx context.Context, dir, prevDir string, now time.Time, readDB func(context.Context, string) (V1CookieDB, error)) (*Jar, bool, error) {
	jarPath := filepath.Join(dir, JarFilename)
	if _, err := os.Stat(jarPath); err == nil {
		if prevDir != "" {
			_ = os.RemoveAll(prevDir)
		}
		return nil, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	dbPath, ok := FindV1CookiesDB(dir)
	if !ok {
		if prevDir != "" {
			_ = os.RemoveAll(prevDir)
		}
		return nil, false, nil
	}
	if readDB == nil {
		return nil, false, errors.New("v1 cookie reader not configured")
	}
	db, err := readDB(ctx, dbPath)
	if err != nil {
		return nil, false, err
	}
	cookies, err := DecodeV1Cookies(db)
	if err != nil {
		return nil, false, err
	}
	jar, encoded, err := NewJarFromCookies(cookies, now)
	if err != nil {
		return nil, false, err
	}
	if err := SaveJarBytes(jarPath, encoded); err != nil {
		return nil, false, err
	}
	if err := CleanupV1Profile(dir, prevDir); err != nil {
		return jar, true, err
	}
	return jar, true, nil
}

// CleanupV1Profile removes all v1 profile entries in dir except jar.json, and removes prevDir.
func CleanupV1Profile(dir, prevDir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == JarFilename {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if prevDir != "" {
		if err := os.RemoveAll(prevDir); err != nil {
			return err
		}
	}
	return nil
}
