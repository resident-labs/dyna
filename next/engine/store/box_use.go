package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/resident-labs/dyna/contract"
)

type boxUse uint8

const (
	boxUseUnknown boxUse = iota
	boxUnused
	boxUsed
)

// boxUseEvidence owns the shared never-ready/no-turn proof for automatic retry and volume cleanup.
// Initial typed placement proves the beginning of the box's history. Missing legacy/import history is
// unknown, never permission to relocate or destroy work. Image and mutable placement times prove nothing.
func boxUseEvidence(ctx context.Context, tx *sql.Tx, id contract.BoxID, room contract.RoomID) (boxUse, error) {
	var known, used, laterCreate bool
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM events WHERE room=? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')=? AND json_extract(CAST(body AS TEXT),'$.to')='pending' AND coalesce(json_extract(CAST(body AS TEXT),'$.from'),'')=''),
 EXISTS(SELECT 1 FROM events WHERE room=? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')=? AND json_extract(CAST(body AS TEXT),'$.to')='ready') OR EXISTS(SELECT 1 FROM turns WHERE session=(SELECT session FROM boxes WHERE id=?)),
 EXISTS(SELECT 1 FROM events WHERE room=? AND type='box.changed' AND json_extract(CAST(body AS TEXT),'$.box')=? AND (json_extract(CAST(body AS TEXT),'$.to') IN ('stopped','lost') OR json_extract(CAST(body AS TEXT),'$.from') IN ('stopped','lost') OR (json_extract(CAST(body AS TEXT),'$.from')='failed' AND json_extract(CAST(body AS TEXT),'$.to')='pending')))`, room, id, room, id, id, room, id).Scan(&known, &used, &laterCreate)
	if err != nil {
		return boxUseUnknown, err
	}
	if used {
		return boxUsed, nil
	}
	// A typed restart/recovery boundary is not positive first-create proof,
	// regardless of who requested it. Owner provenance only ends affinity.
	if known && !laterCreate {
		return boxUnused, nil
	}
	return boxUseUnknown, nil
}

// CanRemoveBoxVolumes reads policy; its result does not own a physical namespace. A caller deleting
// volumes while a session can wake must use RemoveBoxVolumes, not carry this boolean across host I/O.
func (s *Store) CanRemoveBoxVolumes(ctx context.Context, id contract.BoxID) (bool, error) {
	var allowed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		allowed, err = canRemoveBoxVolumes(ctx, tx, id)
		return err
	})
	return allowed, err
}

// canRemoveBoxVolumes preserves close/discard authority and the shared positive unused proof for Stop.
// It is also the proof in the write that claims the namespace; no event, Image or cache grants authority.
func canRemoveBoxVolumes(ctx context.Context, tx *sql.Tx, id contract.BoxID) (bool, error) {
	var room contract.RoomID
	var want BoxWant
	if err := tx.QueryRowContext(ctx, `SELECT s.room,b.want FROM boxes b JOIN sessions s ON s.id=b.session WHERE b.id=?`, id).Scan(&room, &want); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		// Deleted rooms retain explicitly authorized offline debt after their box/session rows go.
		// A missing box alone, or failure debt in an existing room, is not deletion permission.
		var allowed bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM room_leftovers l WHERE l.box=? AND NOT EXISTS(SELECT 1 FROM rooms r WHERE r.id=l.room))`, id).Scan(&allowed)
		return allowed, err
	}
	if want == WantClose || want == WantDiscard {
		return true, nil
	}
	if want != WantStop {
		return false, nil
	}
	proof, err := boxUseEvidence(ctx, tx, id, room)
	return proof == boxUnused, err
}
