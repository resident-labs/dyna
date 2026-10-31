package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"time"
)

var FullDeviceScopes = []contract.Scope{contract.ScopeRead, contract.ScopeOperate, contract.ScopeFiles, contract.ScopeTerminal, contract.ScopeComputers, contract.ScopeAdmin}

type Device struct {
	ID         string
	Name       string
	TokenHash  [32]byte
	PairedAt   time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	Scopes     []contract.Scope
}

const DeviceLife = 30 * 24 * time.Hour

func (s *Store) CreateDevice(ctx context.Context, d Device) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO devices(id,name,token_sha256,paired_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?)", d.ID, d.Name, d.TokenHash[:], d.PairedAt.UnixMilli(), d.LastSeenAt.UnixMilli(), d.ExpiresAt.UnixMilli())
		if err != nil {
			return err
		}
		for _, scope := range d.Scopes {
			if _, err := tx.ExecContext(ctx, "INSERT INTO device_scopes(device,scope) VALUES(?,?)", d.ID, scope); err != nil {
				return err
			}
		}
		return nil
	})
}

// AuthenticateDevice checks revocation and expiry on every request. Renewals
// use a conditional write so concurrent requests cannot renew more than daily.
func (s *Store) AuthenticateDevice(ctx context.Context, hash [32]byte, now time.Time) (Device, bool, error) {
	var d Device
	var paired, seen, expires int64
	err := s.read.QueryRowContext(ctx, "SELECT id,name,paired_at,last_seen_at,expires_at FROM devices WHERE token_sha256=? AND revoked_at IS NULL AND expires_at>?", hash[:], now.UnixMilli()).Scan(&d.ID, &d.Name, &paired, &seen, &expires)
	if err == sql.ErrNoRows {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	d.TokenHash = hash
	d.PairedAt = time.UnixMilli(paired)
	d.LastSeenAt = time.UnixMilli(seen)
	d.ExpiresAt = time.UnixMilli(expires)
	if now.UnixMilli()-seen >= (24 * time.Hour).Milliseconds() {
		err = s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE devices SET last_seen_at=?,expires_at=? WHERE id=? AND revoked_at IS NULL AND expires_at>? AND last_seen_at<=?", now.UnixMilli(), now.Add(DeviceLife).UnixMilli(), d.ID, now.UnixMilli(), now.Add(-24*time.Hour).UnixMilli())
			return err
		})
		if err != nil {
			return Device{}, false, err
		}
	}
	rows, err := s.read.QueryContext(ctx, "SELECT scope FROM device_scopes WHERE device=? ORDER BY scope", d.ID)
	if err != nil {
		return Device{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope contract.Scope
		if err := rows.Scan(&scope); err != nil {
			return Device{}, false, err
		}
		d.Scopes = append(d.Scopes, scope)
	}
	return d, true, rows.Err()
}
