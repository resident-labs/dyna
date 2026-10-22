package webpush

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"errors"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// TODO: legacy/shim remove later — old releases read push.json; remove when requirement 31 ends.
const subsFile = "push.json"

// Subscription is what the browser gives: the endpoint and its two keys.
// P256dh and Auth are base64url.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Entry is one stored subscription.
type Entry struct {
	DeviceID string    `json:"deviceId"`
	Endpoint string    `json:"endpoint"`
	P256dh   string    `json:"p256dh"`
	Auth     string    `json:"auth"`
	Names    bool      `json:"names"`
	Created  time.Time `json:"created"`
}

// ErrNotFound means the device has no subscription.
var ErrNotFound = errors.New("webpush: no subscription for this device")

// Store keeps one subscription per device in the shared dyna.db.
type Store struct {
	db  *sql.DB
	now func() time.Time
	// allow checks an endpoint. Tests replace it; otherwise it is ValidateEndpoint.
	allow func(string) error
}

// OpenStore opens subscriptions in dataDir. The engine migrates them at start.
func OpenStore(dataDir string) (*Store, error) {
	db, err := openDB(dataDir)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, now: time.Now, allow: ValidateEndpoint}, nil
}

// Subscribe checks and stores sub for the device. The last subscribe wins.
func (s *Store) Subscribe(deviceID string, sub Subscription, names bool) error {
	if deviceID == "" {
		return errors.New("webpush: device ID is empty")
	}
	if err := s.allow(sub.Endpoint); err != nil {
		return err
	}
	if _, _, err := parseKeys(sub.P256dh, sub.Auth); err != nil {
		return err
	}
	e := Entry{DeviceID: deviceID, Endpoint: sub.Endpoint, P256dh: sub.P256dh,
		Auth: sub.Auth, Names: names, Created: s.now().UTC()}
	return s.write(func(conn *sql.Conn) error { return saveEntry(conn, e) })
}

// parseKeys checks the p256dh point and the auth secret.
func parseKeys(p256dh, auth string) (*ecdh.PublicKey, []byte, error) {
	raw, err := decodeB64(p256dh)
	if err != nil {
		return nil, nil, errors.New("webpush: p256dh is not base64url")
	}
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, nil, errors.New("webpush: p256dh is not a P-256 point")
	}
	a, err := decodeB64(auth)
	if err != nil || len(a) != 16 {
		return nil, nil, errors.New("webpush: auth must be 16 bytes of base64url")
	}
	return pub, a, nil
}

// Unsubscribe removes the device's subscription. A missing entry is not an error.
func (s *Store) Unsubscribe(deviceID string) error { return s.remove(deviceID, "") }

// remove only deletes the endpoint that failed, so a new subscribe is not lost.
func (s *Store) remove(deviceID, endpoint string) error {
	return s.write(func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `DELETE FROM push_subscriptions
   WHERE device_id=? AND (?='' OR endpoint=?)`, deviceID, endpoint, endpoint)
		return err
	})
}

// SetNames sets whether messages show room and sender names for this device.
func (s *Store) SetNames(deviceID string, names bool) error {
	return s.write(func(conn *sql.Conn) error {
		result, err := conn.ExecContext(context.Background(), "UPDATE push_subscriptions SET names=? WHERE device_id=?", names, deviceID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
}

// Get returns the device's subscription and any read error.
func (s *Store) Get(deviceID string) (Entry, bool, error) {
	e, err := scanEntry(s.db.QueryRow("SELECT "+entryColumns+" FROM push_subscriptions WHERE device_id=?", deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, false, nil
	}
	return e, err == nil, err
}

// List returns subscriptions in device ID order and any read error.
func (s *Store) List() ([]Entry, error) {
	rows, err := s.db.Query("SELECT " + entryColumns + " FROM push_subscriptions ORDER BY device_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// RemoveDevicesNotIn removes subscriptions that keep rejects. The callback runs
// before the write, so it can query other stores without holding the writer.
func (s *Store) RemoveDevicesNotIn(keep func(deviceID string) bool) (int, error) {
	entries, err := s.List()
	if err != nil {
		return 0, err
	}
	var gone []Entry
	for _, e := range entries {
		if !keep(e.DeviceID) {
			gone = append(gone, e)
		}
	}
	if len(gone) == 0 {
		return 0, nil
	}
	removed := 0
	err = s.write(func(conn *sql.Conn) error {
		for _, e := range gone {
			result, err := conn.ExecContext(context.Background(), `DELETE FROM push_subscriptions
    WHERE device_id=? AND endpoint=? AND created=?`, e.DeviceID, e.Endpoint, e.Created.Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			removed += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// write commits a subscription change through the shared state writer.
func (s *Store) write(update func(*sql.Conn) error) error {
	return state.For(s.db).Write(context.Background(), hub.Event{}, update)
}
