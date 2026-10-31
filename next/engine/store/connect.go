package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// SetConnectConnector emits a typed event only when the missing state changes.
func (s *Store) SetConnectConnector(ctx context.Context, missing bool, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var previous bool
		err := tx.QueryRowContext(ctx, "SELECT missing FROM connect_connector WHERE id=1").Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previous == missing {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO connect_connector(id,missing) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET missing=excluded.missing", missing); err != nil {
			return err
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, contract.Event{ID: contract.EventID("ev_" + hex.EncodeToString(id[:])), Time: at, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.ConnectConnector{Missing: missing}})
		return err
	})
}

func (s *Store) ConnectConnectorMissing(ctx context.Context) (bool, error) {
	var missing bool
	err := s.read.QueryRowContext(ctx, "SELECT missing FROM connect_connector WHERE id=1").Scan(&missing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return missing, err
}

// ConnectAttempt reads the public status, never the approval material.
func (s *Store) ConnectAttempt(ctx context.Context) (contract.ConnectAttempt, error) {
	var a contract.ConnectAttempt
	err := s.read.QueryRowContext(ctx, "SELECT state,expires FROM connect_attempt WHERE id=1").Scan(&a.State, &a.Expires)
	if errors.Is(err, sql.ErrNoRows) {
		a.State = contract.ConnectAttemptIdle
		return a, nil
	}
	return a, err
}

func (s *Store) SetConnectAttempt(ctx context.Context, a contract.ConnectAttempt) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO connect_attempt(id,state,expires) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,expires=excluded.expires", a.State, a.Expires)
		return err
	})
}

// ConnectLinkState is the durable revocation boundary for a machine link.
type ConnectLinkState struct {
	Epoch      int64
	Generation int64
	Linked     bool
}

var ErrConnectEpoch = errors.New("connect link is inactive or epoch is stale")

// InitializeConnectLink starts a new link after the relay approves it.
func (s *Store) InitializeConnectLink(ctx context.Context, epoch int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "INSERT INTO connect_link_state(id,epoch,linked,generation) VALUES(1,?,1,1) ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,linked=1,generation=generation+1 WHERE linked=0", epoch)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("connect link is already active")
		}
		return nil
	})
}

// UnlinkConnect invalidates the link and all replay nonces atomically.
func (s *Store) UnlinkConnect(ctx context.Context) error { return s.unlinkConnect(ctx, 0) }

// UnlinkConnectGeneration ignores a response for a replaced link.
func (s *Store) UnlinkConnectGeneration(ctx context.Context, generation int64) error {
	return s.unlinkConnect(ctx, generation)
}

func (s *Store) unlinkConnect(ctx context.Context, generation int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE connect_link_state SET linked=0 WHERE id=1 AND linked=1 AND (?=0 OR generation=?)", generation, generation)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if generation != 0 && n != 1 {
			return ErrConnectEpoch
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM connect_revocations"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM connect_nonces"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM connect_sessions")
		return err
	})
}

// ConsumeConnectNonceForLink advances the epoch and rejects replayed nonces.
func (s *Store) ConsumeConnectNonceForLink(ctx context.Context, epoch, generation int64, nonce string, now, expires time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM connect_nonces WHERE expires_at<?", now.Unix()); err != nil {
			return err
		}
		r, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO connect_nonces(nonce,expires_at) VALUES(?,?)", nonce, expires.Unix())
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("replayed relay request")
		}
		r, err = tx.ExecContext(ctx, "UPDATE connect_link_state SET epoch=? WHERE id=1 AND linked=1 AND epoch<=? AND generation=?", epoch, epoch, generation)
		if err != nil {
			return err
		}
		n, err = r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConnectEpoch
		}
		return nil
	})
}

// AdvanceConnectEpoch invalidates older sessions without permitting rollback.
func (s *Store) AdvanceConnectEpoch(ctx context.Context, epoch, generation int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE connect_link_state SET epoch=? WHERE id=1 AND linked=1 AND epoch<=? AND generation=?", epoch, epoch, generation)
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

func (s *Store) ConnectLinked(ctx context.Context) (bool, error) {
	var linked bool
	err := s.read.QueryRowContext(ctx, "SELECT linked FROM connect_link_state WHERE id=1").Scan(&linked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return linked, err
}

func (s *Store) ConnectLink(ctx context.Context) (ConnectLinkState, error) {
	var state ConnectLinkState
	err := s.read.QueryRowContext(ctx, "SELECT epoch,linked,generation FROM connect_link_state WHERE id=1").Scan(&state.Epoch, &state.Linked, &state.Generation)
	return state, err
}
