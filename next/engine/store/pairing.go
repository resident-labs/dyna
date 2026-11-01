package store

import (
	"context"
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
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var ErrPair = errors.New("invalid pairing code")

// ErrPairEnded says that no live code exists: it was used, expired, or took 5 wrong tries.
var ErrPairEnded = fmt.Errorf("%w: it ended", ErrPair)
var pairEncoding = base32.NewEncoding("ABCDEFGHJKLMNPQRSTUVWXYZ23456789").WithPadding(base32.NoPadding)

func (s *Store) NewPairCode(ctx context.Context, scopes []contract.Scope, now time.Time) (string, error) {
	raw := make([]byte, 7)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := pairEncoding.EncodeToString(raw)[:10]
	hash := sha256.Sum256([]byte(code))
	body, err := json.Marshal(scopes)
	if err != nil {
		return "", err
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO pairing_codes(id,code_hash,scopes,expires_at,tries) VALUES(1,?,?,?,0) ON CONFLICT(id) DO UPDATE SET code_hash=excluded.code_hash,scopes=excluded.scopes,expires_at=excluded.expires_at,tries=0", hash[:], body, now.Add(5*time.Minute).UnixMilli())
		return err
	})
	return code, err
}

// RedeemPairCode consumes the code and creates the device in one transaction.
// A nil scope grant uses defaults selected by the caller's route.
func (s *Store) RedeemPairCode(ctx context.Context, code, name string, defaults []contract.Scope, now time.Time) (string, Device, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return "", Device{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:32])
	id := "device_" + hex.EncodeToString(raw[32:])
	hash := sha256.Sum256([]byte(strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(code))))
	tokenHash := sha256.Sum256([]byte(token))
	d := Device{ID: id, Name: name, TokenHash: tokenHash, PairedAt: now, LastSeenAt: now, ExpiresAt: now.Add(DeviceLife)}
	var failed error
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var stored, scopes []byte
		var expires int64
		var tries int
		err := tx.QueryRowContext(ctx, "SELECT code_hash,scopes,expires_at,tries FROM pairing_codes WHERE id=1").Scan(&stored, &scopes, &expires, &tries)
		if err == sql.ErrNoRows {
			failed = ErrPairEnded
			return nil
		}
		if err != nil {
			return err
		}
		if expires <= now.UnixMilli() || tries >= 5 {
			failed = ErrPairEnded
			_, err = tx.ExecContext(ctx, "DELETE FROM pairing_codes WHERE id=1")
			return err
		}
		if subtle.ConstantTimeCompare(hash[:], stored) != 1 {
			failed = ErrPair
			if tries+1 >= 5 {
				_, err = tx.ExecContext(ctx, "DELETE FROM pairing_codes WHERE id=1")
			} else {
				_, err = tx.ExecContext(ctx, "UPDATE pairing_codes SET tries=tries+1 WHERE id=1")
			}
			return err
		}
		if err = json.Unmarshal(scopes, &d.Scopes); err != nil {
			return err
		}
		if d.Scopes == nil {
			d.Scopes = defaults
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM pairing_codes WHERE id=1"); err != nil {
			return err
		}
		return insertDevice(ctx, tx, d)
	})
	if err != nil {
		return "", Device{}, err
	}
	if failed != nil {
		return "", Device{}, failed
	}
	return token, d, nil
}

func (s *Store) ListDevices(ctx context.Context, now time.Time) ([]Device, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT id,name,paired_at,last_seen_at,expires_at FROM devices WHERE revoked_at IS NULL AND expires_at>? ORDER BY paired_at,id", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Device{}
	for rows.Next() {
		var d Device
		var paired, seen, expires int64
		if err := rows.Scan(&d.ID, &d.Name, &paired, &seen, &expires); err != nil {
			return nil, err
		}
		d.PairedAt = time.UnixMilli(paired)
		d.LastSeenAt = time.UnixMilli(seen)
		d.ExpiresAt = time.UnixMilli(expires)
		list = append(list, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		scopes, err := s.read.QueryContext(ctx, "SELECT scope FROM device_scopes WHERE device=? ORDER BY scope", list[i].ID)
		if err != nil {
			return nil, err
		}
		for scopes.Next() {
			var scope contract.Scope
			if err := scopes.Scan(&scope); err != nil {
				scopes.Close()
				return nil, err
			}
			list[i].Scopes = append(list[i].Scopes, scope)
		}
		err = scopes.Err()
		scopes.Close()
		if err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (s *Store) RevokeDevice(ctx context.Context, id string, now time.Time) (bool, error) {
	var found bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE devices SET revoked_at=? WHERE id=? AND revoked_at IS NULL", now.UnixMilli(), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		found = n > 0
		return err
	})
	return found, err
}

func (s *Store) SetDeviceScopes(ctx context.Context, id string, scopes []contract.Scope) (bool, error) {
	var found bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE id=? AND revoked_at IS NULL", id).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		found = true
		if _, err := tx.ExecContext(ctx, "DELETE FROM device_scopes WHERE device=?", id); err != nil {
			return err
		}
		for _, scope := range scopes {
			if _, err := tx.ExecContext(ctx, "INSERT INTO device_scopes(device,scope) VALUES(?,?)", id, scope); err != nil {
				return err
			}
		}
		return nil
	})
	return found, err
}
