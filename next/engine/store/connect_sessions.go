package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ConnectSession binds a relay device, browser key and cookie to a scoped device.
type ConnectSession struct {
	ID, RelayDevice, Device, Origin, JKT, User string
	Epoch, Generation                          int64
	Hash                                       [32]byte
	Created, Access, End, Grant, Minted        time.Time
	Stale, CookieStale                         bool
}

// StartConnectSession atomically maps a relay device and replaces its session.
// A revoked mapping is never revived, and existing scopes are never broadened.
func (s *Store) StartConnectSession(ctx context.Context, x ConnectSession) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var epoch, generation int64
		if err := tx.QueryRowContext(ctx, "SELECT epoch,generation FROM connect_link_state WHERE id=1 AND linked=1").Scan(&epoch, &generation); err != nil {
			return err
		}
		if epoch != x.Epoch || generation != x.Generation {
			return ErrConnectEpoch
		}
		var revokedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT revoked_at FROM connect_revocations WHERE relay_device=?", x.RelayDevice).Scan(&revokedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Relay signatures allow 60 seconds of clock skew. Do not let an old
		// future-dated mint cross a revocation within that window.
		if revokedAt.Valid && x.Minted.UnixMilli() <= revokedAt.Int64+time.Minute.Milliseconds() {
			return errors.New("Connect mint predates revocation")
		}
		var device string
		var revoked sql.NullInt64
		err = tx.QueryRowContext(ctx, "SELECT d.id,d.revoked_at FROM connect_devices c JOIN devices d ON d.id=c.device WHERE c.relay_device=?", x.RelayDevice).Scan(&device, &revoked)
		newDevice := errors.Is(err, sql.ErrNoRows)
		if err != nil && !newDevice {
			return err
		}
		if revoked.Valid && x.Minted.UnixMilli() <= revoked.Int64+time.Minute.Milliseconds() {
			return errors.New("Connect device revoked after mint")
		}
		if newDevice || revoked.Valid {
			var token [32]byte
			if _, err := rand.Read(token[:]); err != nil {
				return err
			}
			previous := device
			device = x.Device
			// Connect uses its own cookie; the paired-device token is never issued.
			d := Device{ID: device, Name: "Connect browser", TokenHash: sha256.Sum256(token[:]), PairedAt: x.Created, LastSeenAt: x.Created, ExpiresAt: x.End}
			if newDevice {
				d.Scopes = FullDeviceScopes
			}
			if err := insertDevice(ctx, tx, d); err != nil {
				return err
			}
			if newDevice {
				if _, err := tx.ExecContext(ctx, "INSERT INTO connect_devices(relay_device,device) VALUES(?,?)", x.RelayDevice, device); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, "INSERT INTO device_scopes(device,scope) SELECT ?,scope FROM device_scopes WHERE device=?", device, previous); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "UPDATE connect_devices SET device=? WHERE relay_device=?", device, x.RelayDevice); err != nil {
					return err
				}
			}
		} else if _, err := tx.ExecContext(ctx, "UPDATE devices SET expires_at=?,last_seen_at=? WHERE id=?", x.End.UnixMilli(), x.Created.UnixMilli(), device); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM connect_sessions WHERE relay_device=?", x.RelayDevice); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO connect_sessions(id,relay_device,origin,jkt,user_id,epoch,generation,token_hash,prev_until,created_at,access_until,grant_end,grant_at) VALUES(?,?,?,?,?,?,?,?,0,?,?,?,?)", x.ID, x.RelayDevice, x.Origin, x.JKT, x.User, x.Epoch, x.Generation, x.Hash[:], x.Created.UnixMilli(), x.Access.UnixMilli(), x.End.UnixMilli(), x.Grant.UnixMilli())
		return err
	})
}

// HandoffConnectSession replaces only the still-live source session, retaining its grant and device.
func (s *Store) HandoffConnectSession(ctx context.Context, source string, x ConnectSession, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE connect_sessions SET id=?,origin=?,jkt=?,token_hash=?,old1=NULL,old2=NULL,old3=NULL,prev_until=0
 WHERE id=? AND relay_device=? AND epoch=? AND generation=? AND access_until>? AND grant_end>?
 AND EXISTS(SELECT 1 FROM connect_link_state WHERE id=1 AND linked=1 AND epoch=? AND generation=?)
 AND EXISTS(SELECT 1 FROM connect_devices m JOIN devices d ON d.id=m.device WHERE m.relay_device=connect_sessions.relay_device AND d.revoked_at IS NULL AND d.expires_at>?)`, x.ID, x.Origin, x.JKT, x.Hash[:], source, x.RelayDevice, x.Epoch, x.Generation, now.UnixMilli(), now.UnixMilli(), x.Epoch, x.Generation, now.UnixMilli())
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConnectEpoch
		}
		return nil
	})
}

// AuthenticateConnect reads session, device state and scopes in one snapshot.
// Old cookies identify a session for proof-based renewal but cannot log out.
func (s *Store) AuthenticateConnect(ctx context.Context, hash [32]byte, origin string, now time.Time) (ConnectSession, []contract.Scope, bool, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ConnectSession{}, nil, false, err
	}
	defer tx.Rollback()
	var x ConnectSession
	var created, access, end, grant, prev int64
	var which int
	err = tx.QueryRowContext(ctx, `SELECT c.id,c.relay_device,d.id,c.origin,c.jkt,c.user_id,c.epoch,c.generation,c.created_at,c.access_until,c.grant_end,c.grant_at,c.prev_until, CASE WHEN c.token_hash=? THEN 0 WHEN c.old1=? THEN 1 ELSE 2 END
 FROM connect_sessions c JOIN connect_devices m ON m.relay_device=c.relay_device JOIN devices d ON d.id=m.device JOIN connect_link_state l ON l.id=1
 WHERE (c.token_hash=? OR c.old1=? OR c.old2=? OR c.old3=?) AND c.origin=? AND l.linked=1 AND c.epoch=l.epoch AND c.generation=l.generation AND d.revoked_at IS NULL AND d.expires_at>? AND c.grant_end>?`, hash[:], hash[:], hash[:], hash[:], hash[:], hash[:], origin, now.UnixMilli(), now.UnixMilli()).Scan(&x.ID, &x.RelayDevice, &x.Device, &x.Origin, &x.JKT, &x.User, &x.Epoch, &x.Generation, &created, &access, &end, &grant, &prev, &which)
	if errors.Is(err, sql.ErrNoRows) {
		return x, nil, false, nil
	}
	if err != nil {
		return x, nil, false, err
	}
	x.Hash = hash
	x.Created = time.UnixMilli(created)
	x.Access = time.UnixMilli(access)
	x.End = time.UnixMilli(end)
	x.Grant = time.UnixMilli(grant)
	x.CookieStale = which > 1 || (which == 1 && now.UnixMilli() > prev)
	x.Stale = x.CookieStale || !now.Before(x.Access)
	scopes, err := readDeviceScopes(ctx, tx, x.Device)
	if err != nil {
		return x, nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return x, nil, false, err
	}
	return x, scopes, true, nil
}

// RotateConnectSession rotates only a still-live session and preserves a bounded cookie history.
func (s *Store) RotateConnectSession(ctx context.Context, x ConnectSession, hash [32]byte, now time.Time, grace bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE connect_sessions SET old3=old2,old2=old1,old1=token_hash,token_hash=?,prev_until=?,access_until=?,grant_at=CASE WHEN ? THEN grant_at ELSE ? END
  WHERE id=? AND epoch=? AND generation=? AND grant_end>? AND EXISTS(SELECT 1 FROM connect_link_state WHERE id=1 AND linked=1 AND epoch=? AND generation=?) AND EXISTS(SELECT 1 FROM connect_devices m JOIN devices d ON d.id=m.device WHERE m.relay_device=connect_sessions.relay_device AND d.revoked_at IS NULL AND d.expires_at>?)`, hash[:], now.Add(30*time.Second).UnixMilli(), min(now.Add(15*time.Minute).UnixMilli(), x.End.UnixMilli()), grace, now.UnixMilli(), x.ID, x.Epoch, x.Generation, now.UnixMilli(), x.Epoch, x.Generation, now.UnixMilli())
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConnectEpoch
		}
		return nil
	})
}

// ConnectStreamScopes rechecks the live session and current scopes without relying on a rotated cookie.
func (s *Store) ConnectStreamScopes(ctx context.Context, id string, now time.Time) ([]contract.Scope, bool, error) {
	tx, err := s.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var device string
	err = tx.QueryRowContext(ctx, `SELECT d.id FROM connect_sessions c JOIN connect_devices m ON m.relay_device=c.relay_device JOIN devices d ON d.id=m.device JOIN connect_link_state l ON l.id=1 WHERE c.id=? AND l.linked=1 AND c.epoch=l.epoch AND c.generation=l.generation AND d.revoked_at IS NULL AND d.expires_at>? AND c.grant_end>? AND c.access_until>?`, id, now.UnixMilli(), now.UnixMilli(), now.UnixMilli()).Scan(&device)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	scopes, err := readDeviceScopes(ctx, tx, device)
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return scopes, true, nil
}

func (s *Store) EndConnectSession(ctx context.Context, id string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM connect_sessions WHERE id=?", id)
		return err
	})
}

func (s *Store) RevokeConnectDevice(ctx context.Context, relayDevice string, generation int64, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var current int64
		var linked bool
		if err := tx.QueryRowContext(ctx, "SELECT generation,linked FROM connect_link_state WHERE id=1").Scan(&current, &linked); err != nil {
			return err
		}
		if generation != current {
			return ErrConnectEpoch
		}
		if !linked {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO connect_revocations(relay_device,revoked_at) VALUES(?,?) ON CONFLICT(relay_device) DO UPDATE SET revoked_at=MAX(revoked_at,excluded.revoked_at)", relayDevice, now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE devices SET revoked_at=? WHERE id=(SELECT device FROM connect_devices WHERE relay_device=?) AND revoked_at IS NULL", now.UnixMilli(), relayDevice); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM connect_sessions WHERE relay_device=?", relayDevice)
		return err
	})
}
