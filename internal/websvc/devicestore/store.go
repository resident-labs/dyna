// Package devicestore keeps paired browser devices and pairing codes. It has
// no heavy imports, so the computer daemon can sign its browser in.
package devicestore

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/store"
)

// CookieName holds the device token.
const CookieName = "dyna_device"

// Pairing and device rules. A code is 10 base32 characters (50 bits). It
// lives 5 minutes, and 5 wrong tries kill it. Only the local owner mints a
// code (dyna engine run --pair); no HTTP route can.
const (
	CodeLife     = 5 * time.Minute
	CodeTries    = 5
	DeviceLife   = 30 * 24 * time.Hour
	seenEvery    = time.Minute
	maxNameBytes = 40
)

// RenewEvery is how often a used full-life device's expiry slides forward
// again. The server sends a fresh cookie at the same moment, so a browser
// in use never has to pair again.
const RenewEvery = 24 * time.Hour

// ErrPair is the one answer to every failed pairing try: no code, an expired
// code, a dead code or a wrong code all look the same.
var ErrPair = errors.New("that code is not valid; run dyna engine run --pair on the computer for a new one")

// codeAlphabet has no 0, 1, I or O, so a code typed on a phone is not
// misread.
var codeEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

// Device is one paired browser. The token itself is never stored, only its
// SHA-256.
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Hash     string    `json:"hash"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
	// Scopes is the grant stored with the pairing code that created this
	// device. It is returned to the caller and is not written to devices.json.
	Scopes []string `json:"-"`
}

type pairing struct {
	ID      string    `json:"id,omitempty"`
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
	Tries   int       `json:"tries"`
	Scopes  []string  `json:"scopes,omitempty"`
}

// Store keeps the web server's state in <datadir>/web, mode 0700:
//
//	key           32 random bytes that sign card tickets
//	pair.json     the live pairing code's HMAC (keyed by key), expiry and
//	              wrong tries, so a reader of the file has nothing to guess
//	devices.json  paired devices, token hashes only
//
// The server and the --pair, --devices and --revoke commands are separate
// processes. Every change takes an exclusive flock on web/.lock and replaces
// the file by rename, so no reader sees a partial write. The server reads
// devices.json again whenever its size or time changes, so a revoke takes
// effect on the next request.
type Store struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	stamp   string
	devices []Device
}

// DefaultStore opens the web state directory in the Dyna data directory.
func DefaultStore() (*Store, error) {
	return OpenStore(filepath.Join(store.DataDir(), "web"))
}

// OpenStore opens (and makes) the state directory.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now}, nil
}

// Dir is the store's state directory.
func (s *Store) Dir() string { return s.dir }

// Now is the store's clock. The server's tickets share it.
func (s *Store) Now() time.Time { return s.now() }

// SetClock replaces the store's clock, so tests can expire codes and devices.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func (s *Store) locked(fn func() error) error {
	f, err := os.OpenFile(s.path(".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

func (s *Store) write(name string, b []byte) error {
	return fsutil.WriteFile(s.path(name), b, 0o600, false)
}

func (s *Store) writeJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return s.write(name, append(b, '\n'))
}

func (s *Store) readJSON(name string, v any) (bool, error) {
	b, err := os.ReadFile(s.path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

// Key returns the card ticket key, made on first use.
func (s *Store) Key() ([]byte, error) {
	var key []byte
	err := s.locked(func() error {
		b, err := os.ReadFile(s.path("key"))
		if err == nil && len(b) == 32 {
			key = b
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		return s.write("key", key)
	})
	return key, err
}

// codeMAC hides a pairing code behind the web key.
func (s *Store) codeMAC(code string) (string, error) {
	key, err := s.Key()
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("dyna-pair\x00" + code))
	return hex.EncodeToString(m.Sum(nil)), nil
}

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// NormalizeCode drops spaces and dashes and uppercases, so "abcde-fghjk"
// and "ABCDEFGHJK" are one code.
func NormalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return unicode.ToUpper(r)
	}, code)
}

// NewCode mints a pairing code and kills any older one. It returns the code
// once, as "XXXXX-XXXXX"; only its hash is stored. A nonempty device ID
// replaces that device on pairing instead of adding a new device.
func (s *Store) NewCode(deviceID string) (string, error) {
	return s.NewScopedCode(deviceID, nil)
}

// NewScopedCode mints a pairing code that carries scopes. NewCode carries none.
// A nonempty device ID replaces that device on pairing instead of adding one.
func (s *Store) NewScopedCode(deviceID string, scopes []string) (string, error) {
	raw := make([]byte, 7)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := codeEncoding.EncodeToString(raw)[:10]
	mac, err := s.codeMAC(code)
	if err != nil {
		return "", err
	}
	kept := append([]string(nil), scopes...)
	err = s.locked(func() error {
		return s.writeJSON("pair.json", pairing{ID: deviceID, Hash: mac, Expires: s.now().Add(CodeLife), Scopes: kept})
	})
	if err != nil {
		return "", err
	}
	return code[:5] + "-" + code[5:], nil
}

// Pair spends a pairing code. On success the code dies and a new device
// gets a token, returned once. Every failure is ErrPair. A wrong try counts
// only while a code lives.
func (s *Store) Pair(code, name string) (token string, dev Device, err error) {
	mac, err := s.codeMAC(NormalizeCode(code))
	if err != nil {
		return "", Device{}, err
	}
	err = s.locked(func() error {
		var p pairing
		ok, err := s.readJSON("pair.json", &p)
		if err != nil {
			return err
		}
		now := s.now()
		if !ok || p.Hash == "" || !now.Before(p.Expires) || p.Tries >= CodeTries {
			if ok {
				_ = os.Remove(s.path("pair.json"))
			}
			return ErrPair
		}
		if subtle.ConstantTimeCompare([]byte(mac), []byte(p.Hash)) != 1 {
			p.Tries++
			if p.Tries >= CodeTries {
				_ = os.Remove(s.path("pair.json"))
			} else if err := s.writeJSON("pair.json", p); err != nil {
				return err
			}
			return ErrPair
		}
		if err := os.Remove(s.path("pair.json")); err != nil {
			return err
		}
		token, dev, err = s.addDevice(p.ID, name, DeviceLife)
		if err != nil {
			return err
		}
		dev.Scopes = append([]string(nil), p.Scopes...)
		return nil
	})
	if err != nil {
		return "", Device{}, err
	}
	return token, dev, nil
}

// IssueDevice adds a trusted device that lives life and returns its token once. A nonempty id
// replaces the device with that ID. It never touches a pending pairing code, so it cannot cancel
// a code the owner minted.
func (s *Store) IssueDevice(id, name string, life time.Duration) (token string, dev Device, err error) {
	err = s.locked(func() error {
		var err error
		token, dev, err = s.addDevice(id, name, life)
		return err
	})
	return token, dev, err
}

// addDevice makes the device and token, and replaces the device with the same ID. The caller holds the store lock.
func (s *Store) addDevice(id, name string, life time.Duration) (token string, dev Device, err error) {
	tok := make([]byte, 32)
	rid := make([]byte, 4)
	if _, err := rand.Read(tok); err != nil {
		return "", Device{}, err
	}
	if _, err := rand.Read(rid); err != nil {
		return "", Device{}, err
	}
	now := s.now()
	token = base64.RawURLEncoding.EncodeToString(tok)
	dev = Device{ID: hex.EncodeToString(rid), Name: CleanName(name), Hash: sum(token), Created: now, LastSeen: now, Expires: now.Add(life)}
	if id != "" {
		dev.ID = id
	}
	list, err := s.readDevices()
	if err != nil {
		return "", Device{}, err
	}
	list = slices.DeleteFunc(live(list, now), func(d Device) bool { return d.ID == dev.ID })
	if err := s.writeJSON("devices.json", append(list, dev)); err != nil {
		return "", Device{}, err
	}
	s.forgetCache()
	return token, dev, nil
}

// CleanName keeps a device name safe to print in a terminal: no control
// characters (so no escape sequences) and at most 40 bytes.
func CleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	for len(name) > maxNameBytes {
		r := []rune(name)
		name = string(r[:len(r)-1])
	}
	if name == "" {
		return "browser"
	}
	return name
}

func live(list []Device, now time.Time) []Device {
	out := list[:0:0]
	for _, d := range list {
		if now.Before(d.Expires) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Store) readDevices() ([]Device, error) {
	var list []Device
	if _, err := s.readJSON("devices.json", &list); err != nil {
		return nil, err
	}
	return list, nil
}

// Devices lists live paired devices.
func (s *Store) Devices() ([]Device, error) {
	var list []Device
	err := s.locked(func() error {
		var err error
		list, err = s.readDevices()
		return err
	})
	return live(list, s.now()), err
}

// Revoke removes one device by ID, or every device for "all". It returns
// how many it removed.
func (s *Store) Revoke(id string) (int, error) {
	n := 0
	err := s.locked(func() error {
		list, err := s.readDevices()
		if err != nil {
			return err
		}
		keep := list[:0:0]
		for _, d := range list {
			if id == "all" || d.ID == id {
				n++
				continue
			}
			keep = append(keep, d)
		}
		if n == 0 {
			return fmt.Errorf("no paired device has the ID %q", id)
		}
		if err := s.writeJSON("devices.json", keep); err != nil {
			return err
		}
		s.forgetCache()
		return nil
	})
	return n, err
}

// RenewDue reports whether this full-life device's expiry should slide
// forward now: a device in use renews at least once every RenewEvery, and
// a deliberately short-lived device (its life is under DeviceLife) never
// renews.
func (d Device) RenewDue(now time.Time) bool {
	return !d.Expires.Before(d.Created.Add(DeviceLife)) && d.Expires.Sub(now) < DeviceLife-RenewEvery
}

// Check finds the live device that holds token. It reads devices.json again
// when the file changed, and writes LastSeen at most once a minute. A
// full-life device in use also has its expiry slid forward to a fresh
// DeviceLife (see RenewDue), so the owner's paired browser stays paired as
// long as it is used.
func (s *Store) Check(token string) (Device, bool) {
	return s.check(token, true)
}

// Alive checks a stream without consuming cookie renewal. A stream cannot
// send a new cookie after its response headers have been written.
func (s *Store) Alive(token string) bool {
	_, ok := s.check(token, false)
	return ok
}

func (s *Store) check(token string, refresh bool) (Device, bool) {
	if token == "" {
		return Device{}, false
	}
	list := s.cached()
	h := sum(token)
	now := s.now()
	for _, d := range list {
		if subtle.ConstantTimeCompare([]byte(d.Hash), []byte(h)) == 1 && now.Before(d.Expires) {
			renew := refresh && d.RenewDue(now)
			if now.Sub(d.LastSeen) >= seenEvery || renew {
				s.touch(d.ID, now, renew)
			}
			return d, true
		}
	}
	return Device{}, false
}

func (s *Store) cached() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	stamp := ""
	if fi, err := os.Stat(s.path("devices.json")); err == nil {
		stamp = fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
	}
	if stamp != s.stamp || stamp == "" {
		list, err := s.readDevices()
		if err != nil {
			// A broken file trusts nobody.
			list = nil
		}
		s.devices, s.stamp = list, stamp
	}
	return s.devices
}

// forgetCache drops the cached device list so the next read reloads the
// file. Two same-size writes can share one (size, mtime) stamp when the
// filesystem's timestamps do not move between them, so the stamp must not
// stand after this store writes.
func (s *Store) forgetCache() {
	s.mu.Lock()
	s.stamp = ""
	s.mu.Unlock()
}

func (s *Store) touch(id string, now time.Time, renew bool) {
	_ = s.locked(func() error {
		list, err := s.readDevices()
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == id {
				list[i].LastSeen = now
				if renew {
					list[i].Expires = now.Add(DeviceLife)
				}
				if err := s.writeJSON("devices.json", list); err != nil {
					return err
				}
				s.forgetCache()
				return nil
			}
		}
		return nil
	})
}

// Forget removes the device that holds token (logout).
func (s *Store) Forget(token string) {
	d, ok := s.Check(token)
	if ok {
		_, _ = s.Revoke(d.ID)
	}
}
