package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ConnectAttempt reads the public status, never the approval material.
func (s *Store) ConnectAttempt(ctx context.Context) (contract.ConnectAttempt, error) {
	var a contract.ConnectAttempt
	err := s.read.QueryRowContext(ctx, "SELECT state,expires FROM connect_attempt WHERE id=1").Scan(&a.State, &a.Expires)
	if errors.Is(err, sql.ErrNoRows) {
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
	Epoch  int64
	Linked bool
}

var ErrConnectEpoch = errors.New("connect link is inactive or epoch is stale")

// InitializeConnectLink starts a new link after the relay approves it.
func (s *Store) InitializeConnectLink(ctx context.Context, epoch int64) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "INSERT INTO connect_link_state(id,epoch,linked) VALUES(1,?,1) ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,linked=1 WHERE linked=0", epoch)
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

// ConsumeConnectNonce advances the epoch and consumes the nonce in one transaction.
func (s *Store) ConsumeConnectNonce(ctx context.Context, epoch int64, nonce string, now, expires time.Time) error {
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
		r, err = tx.ExecContext(ctx, "UPDATE connect_link_state SET epoch=? WHERE id=1 AND linked=1 AND epoch<=?", epoch, epoch)
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
	err := s.read.QueryRowContext(ctx, "SELECT epoch,linked FROM connect_link_state WHERE id=1").Scan(&state.Epoch, &state.Linked)
	return state, err
}
