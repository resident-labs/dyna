// Package devicestore keeps paired browser devices and pairing codes. It has
// no heavy imports, so the computer daemon can sign its browser in.
package devicestore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"dyna-agent/internal/database"
	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
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
	// device. It is returned to the caller and is not stored with the device.
	Scopes []string `json:"-"`
}

type pairing struct {
	ID      string    `json:"id,omitempty"`
	Hash    string    `json:"hash"`
	Expires time.Time `json:"expires"`
	Tries   int       `json:"tries"`
	Scopes  []string  `json:"scopes,omitempty"`
}

// Store keeps paired devices and the live pairing code in dyna.db, in the
// parent of its state directory <datadir>/web (mode 0700). The directory
// holds one file:
//
//	key  32 random bytes that sign card tickets and key the pairing code's
//	     HMAC, so a reader of the database has nothing to guess
//
// The server and the --pair, --devices and --revoke commands are separate
// processes. Every change is one SQLite writer transaction and every read
// sees the latest commit, so a revoke takes effect on the next request.
type Store struct {
	dir string
	now func() time.Time
	w   *state.Writer
}

// DefaultStore opens the web state directory in the Dyna data directory.
func DefaultStore() (*Store, error) {
	return OpenStore(filepath.Join(store.DataDir(), "web"))
}

// Migrate brings the device tables in dataDir's dyna.db to this version and
// imports devices.json and pair.json from <dataDir>/web once. Only the engine
// calls it, at start (requirement 41).
func Migrate(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return migrate(db, filepath.Join(dataDir, "web"))
}

func migrate(db *sql.DB, dir string) error {
	return database.Migrate(db, "web_devices.version", 1, func(conn *sql.Conn, old int) error {
		ctx := context.Background()
		if old != 0 {
			return nil
		}
		for _, stmt := range []string{
			`CREATE TABLE web_devices (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 hash TEXT NOT NULL,
 created TEXT NOT NULL,
 last_seen TEXT NOT NULL,
 expires TEXT NOT NULL
)`,
			`CREATE INDEX web_devices_hash ON web_devices(hash)`,
			`CREATE TABLE web_pairing (
 slot INTEGER PRIMARY KEY CHECK (slot = 1),
 device_id TEXT NOT NULL,
 hash TEXT NOT NULL,
 expires TEXT NOT NULL,
 tries INTEGER NOT NULL,
 scopes TEXT NOT NULL
)`,
		} {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		// TODO: legacy/shim remove later — imports the pre-SQLite devices.json once so paired devices keep working; remove after one release when no install can still hold the file.
		_, err := state.ImportMigration(ctx, conn, "web/devices", filepath.Join(dir, "devices.json"), func(conn *sql.Conn, raw []byte) error {
			var list []Device
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("read devices.json: %w", err)
			}
			for _, d := range list {
				if err := putDevice(ctx, conn, d); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		// TODO: legacy/shim remove later — imports the pre-SQLite pair.json once so a pending pairing code keeps working; remove after one release when no install can still hold the file.
		_, err = state.ImportMigration(ctx, conn, "web/pair", filepath.Join(dir, "pair.json"), func(conn *sql.Conn, raw []byte) error {
			var p pairing
			if err := json.Unmarshal(raw, &p); err != nil {
				return fmt.Errorf("read pair.json: %w", err)
			}
			return putPairing(ctx, conn, p)
		})
		return err
	})
}

// OpenStore opens (and makes) the state directory. dyna.db is in its parent.
// Only the engine creates the tables and runs the import. In another process
// the migration check succeeds once the engine has migrated, and fails with a
// not-migrated error before that.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := database.Open(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	if err := migrate(db, dir); err != nil {
		return nil, err
	}
	return &Store{dir: dir, now: time.Now, w: state.For(db)}, nil
}

// Dir is the store's state directory.
func (s *Store) Dir() string { return s.dir }

// Now is the store's clock. The server's tickets share it.
func (s *Store) Now() time.Time { return s.now() }

// SetClock replaces the store's clock, so tests can expire codes and devices.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// update runs fn in one writer transaction.
func (s *Store) update(fn func(ctx context.Context, conn *sql.Conn) error) error {
	ctx := context.Background()
	return s.w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error { return fn(ctx, conn) })
}

// Key returns the card ticket key, made on first use. Processes make it
// under an exclusive flock on web/.lock, so they all get one key.
func (s *Store) Key() ([]byte, error) {
	f, err := os.OpenFile(s.path(".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	b, err := os.ReadFile(s.path("key"))
	if err == nil && len(b) == 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, fsutil.WriteFile(s.path("key"), key, 0o600, false)
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
	err = s.update(func(ctx context.Context, conn *sql.Conn) error {
		return putPairing(ctx, conn, pairing{ID: deviceID, Hash: mac, Expires: s.now().Add(CodeLife), Scopes: kept})
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
	failed := false
	err = s.update(func(ctx context.Context, conn *sql.Conn) error {
		p, ok, err := readPairing(ctx, conn)
		if err != nil {
			return err
		}
		// A failed try still commits: it kills the code or counts the try.
		if !ok || p.Hash == "" || !s.now().Before(p.Expires) || p.Tries >= CodeTries {
			failed = true
			return deletePairing(ctx, conn)
		}
		if subtle.ConstantTimeCompare([]byte(mac), []byte(p.Hash)) != 1 {
			failed = true
			p.Tries++
			if p.Tries >= CodeTries {
				return deletePairing(ctx, conn)
			}
			_, err := conn.ExecContext(ctx, "UPDATE web_pairing SET tries = ? WHERE slot = 1", p.Tries)
			return err
		}
		if err := deletePairing(ctx, conn); err != nil {
			return err
		}
		token, dev, err = addDevice(ctx, conn, s.now(), p.ID, name, DeviceLife)
		if err != nil {
			return err
		}
		dev.Scopes = append([]string(nil), p.Scopes...)
		return nil
	})
	if err == nil && failed {
		err = ErrPair
	}
	if err != nil {
		return "", Device{}, err
	}
	return token, dev, nil
}

// IssueDevice adds a trusted device that lives life and returns its token once. A nonempty id
// replaces the device with that ID. It never touches a pending pairing code, so it cannot cancel
// a code the owner minted.
func (s *Store) IssueDevice(id, name string, life time.Duration) (token string, dev Device, err error) {
	err = s.update(func(ctx context.Context, conn *sql.Conn) error {
		var err error
		token, dev, err = addDevice(ctx, conn, s.now(), id, name, life)
		return err
	})
	return token, dev, err
}

// addDevice makes the device and token, replaces the device with the same ID
// and drops expired devices.
func addDevice(ctx context.Context, conn *sql.Conn, now time.Time, id, name string, life time.Duration) (token string, dev Device, err error) {
	tok := make([]byte, 32)
	rid := make([]byte, 4)
	if _, err := rand.Read(tok); err != nil {
		return "", Device{}, err
	}
	if _, err := rand.Read(rid); err != nil {
		return "", Device{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(tok)
	dev = Device{ID: hex.EncodeToString(rid), Name: CleanName(name), Hash: sum(token), Created: now, LastSeen: now, Expires: now.Add(life)}
	if id != "" {
		dev.ID = id
	}
	list, err := queryDevices(ctx, conn, "")
	if err != nil {
		return "", Device{}, err
	}
	for _, d := range list {
		if !now.Before(d.Expires) {
			if _, err := conn.ExecContext(ctx, "DELETE FROM web_devices WHERE id = ?", d.ID); err != nil {
				return "", Device{}, err
			}
		}
	}
	return token, dev, putDevice(ctx, conn, dev)
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

// Devices lists live paired devices.
func (s *Store) Devices() ([]Device, error) {
	var list []Device
	err := s.w.Read(context.Background(), func(q state.Reader) error {
		var err error
		list, err = queryDevices(context.Background(), q, "")
		return err
	})
	now := s.now()
	live := list[:0:0]
	for _, d := range list {
		if now.Before(d.Expires) {
			live = append(live, d)
		}
	}
	return live, err
}

// Revoke removes one device by ID, or every device for "all". It returns
// how many it removed.
func (s *Store) Revoke(id string) (int, error) {
	n := 0
	err := s.update(func(ctx context.Context, conn *sql.Conn) error {
		query, args := "DELETE FROM web_devices WHERE id = ?", []any{id}
		if id == "all" {
			query, args = "DELETE FROM web_devices", nil
		}
		res, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return fmt.Errorf("no paired device has the ID %q", id)
		}
		n = int(changed)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RenewDue reports whether this full-life device's expiry should slide
// forward now: a device in use renews at least once every RenewEvery, and
// a deliberately short-lived device (its life is under DeviceLife) never
// renews.
func (d Device) RenewDue(now time.Time) bool {
	return !d.Expires.Before(d.Created.Add(DeviceLife)) && d.Expires.Sub(now) < DeviceLife-RenewEvery
}

// Check finds the live device that holds token. It writes LastSeen at most
// once a minute. A full-life device in use also has its expiry slid forward
// to a fresh DeviceLife (see RenewDue), so the owner's paired browser stays
// paired as long as it is used.
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
	var list []Device
	err := s.w.Read(context.Background(), func(q state.Reader) error {
		var err error
		list, err = queryDevices(context.Background(), q, "WHERE hash = ?", sum(token))
		return err
	})
	now := s.now()
	// A broken database trusts nobody.
	if err != nil || len(list) == 0 || !now.Before(list[0].Expires) {
		return Device{}, false
	}
	d := list[0]
	renew := refresh && d.RenewDue(now)
	if now.Sub(d.LastSeen) >= seenEvery || renew {
		s.touch(d, now, renew)
	}
	return d, true
}

// touch records a use of d. A device revoked or replaced meanwhile stays gone.
// Only a renewal writes the expiry, so a stale snapshot never undoes another
// handle's newer renewal.
func (s *Store) touch(d Device, now time.Time, renew bool) {
	query, args := "UPDATE web_devices SET last_seen = ? WHERE id = ? AND hash = ?", []any{formatTime(now), d.ID, d.Hash}
	if renew {
		query = "UPDATE web_devices SET last_seen = ?, expires = ? WHERE id = ? AND hash = ?"
		args = []any{formatTime(now), formatTime(now.Add(DeviceLife)), d.ID, d.Hash}
	}
	_ = s.update(func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, query, args...)
		return err
	})
}

// Forget removes the device that holds token (logout).
func (s *Store) Forget(token string) {
	d, ok := s.Check(token)
	if ok {
		_, _ = s.Revoke(d.ID)
	}
}

// querier runs a query on a read snapshot or a writer connection.
type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func formatTime(t time.Time) string { return t.Format(time.RFC3339Nano) }

// queryDevices lists devices; where is an optional clause that follows FROM.
func queryDevices(ctx context.Context, q querier, where string, args ...any) ([]Device, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name, hash, created, last_seen, expires FROM web_devices "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Device
	for rows.Next() {
		var d Device
		var created, seen, expires string
		if err := rows.Scan(&d.ID, &d.Name, &d.Hash, &created, &seen, &expires); err != nil {
			return nil, err
		}
		for dst, src := range map[*time.Time]string{&d.Created: created, &d.LastSeen: seen, &d.Expires: expires} {
			if *dst, err = time.Parse(time.RFC3339Nano, src); err != nil {
				return nil, err
			}
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

func putDevice(ctx context.Context, conn *sql.Conn, d Device) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO web_devices (id, name, hash, created, last_seen, expires) VALUES (?, ?, ?, ?, ?, ?)
 ON CONFLICT(id) DO UPDATE SET name = excluded.name, hash = excluded.hash, created = excluded.created,
 last_seen = excluded.last_seen, expires = excluded.expires`,
		d.ID, d.Name, d.Hash, formatTime(d.Created), formatTime(d.LastSeen), formatTime(d.Expires))
	return err
}

func putPairing(ctx context.Context, conn *sql.Conn, p pairing) error {
	scopes, err := json.Marshal(p.Scopes)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO web_pairing (slot, device_id, hash, expires, tries, scopes) VALUES (1, ?, ?, ?, ?, ?)
 ON CONFLICT(slot) DO UPDATE SET device_id = excluded.device_id, hash = excluded.hash, expires = excluded.expires,
 tries = excluded.tries, scopes = excluded.scopes`,
		p.ID, p.Hash, formatTime(p.Expires), p.Tries, string(scopes))
	return err
}

func readPairing(ctx context.Context, conn *sql.Conn) (pairing, bool, error) {
	var p pairing
	var expires, scopes string
	err := conn.QueryRowContext(ctx, "SELECT device_id, hash, expires, tries, scopes FROM web_pairing WHERE slot = 1").
		Scan(&p.ID, &p.Hash, &expires, &p.Tries, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return pairing{}, false, nil
	}
	if err != nil {
		return pairing{}, false, err
	}
	if p.Expires, err = time.Parse(time.RFC3339Nano, expires); err != nil {
		return pairing{}, false, err
	}
	return p, true, json.Unmarshal([]byte(scopes), &p.Scopes)
}

func deletePairing(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM web_pairing")
	return err
}
