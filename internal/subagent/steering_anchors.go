package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"dyna-agent/internal/state"
)

// ErrSteeringChanged means a prepared transcript snapshot is no longer current.
var ErrSteeringChanged = errors.New("steering snapshot changed")

// SteeringAnchorSnapshot binds a turn to its canonical database revision.
// Transcript preparation must keep the original normalized range and steering order.
type SteeringAnchorSnapshot struct {
	Revision int64
	Turn     Turn
}

// SteeringAnchorSnapshots reads canonical turns in turn order, including turns
// absent from a session record. It does not read or import retained turn files.
func (s *Store) SteeringAnchorSnapshots(ctx context.Context, id string) ([]SteeringAnchorSnapshot, error) {
	if _, err := s.Dir(id); err != nil {
		return nil, err
	}
	db, err := s.turnsDB()
	if err != nil {
		return nil, err
	}
	var snapshots []SteeringAnchorSnapshot
	err = state.For(db).Read(ctx, func(r state.Reader) error {
		rows, err := r.QueryContext(ctx, "SELECT n, revision, turn FROM session_turns WHERE session = ? ORDER BY n", id)
		if err != nil {
			return err
		}
		type storedSnapshot struct {
			n        int
			revision int64
			raw      []byte
		}
		var stored []storedSnapshot
		for rows.Next() {
			var row storedSnapshot
			if err := rows.Scan(&row.n, &row.revision, &row.raw); err != nil {
				rows.Close()
				return err
			}
			stored = append(stored, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, row := range stored {
			resolved, err := resolveRowPrompts(ctx, r, id, row.n, row.raw)
			if err != nil {
				return err
			}
			turn, err := s.decodeTurnRecord(id, row.n, resolved)
			if err != nil {
				return err
			}
			if turn.N != row.n {
				return fmt.Errorf("session %s turn %d contains turn %d", id, row.n, turn.N)
			}
			snapshots = append(snapshots, SteeringAnchorSnapshot{Revision: row.revision, Turn: turn})
		}
		return nil
	})
	return snapshots, err
}

// SetSteeringAnchors fills missing historical anchors inside the caller's
// transaction. Preparation runs before that transaction. The caller owns commit
// publication; this method neither starts a transaction nor writes a file copy.
// Existing anchors and active, undelivered entries remain unchanged.
func (s *Store) SetSteeringAnchors(ctx context.Context, conn *sql.Conn, id string, want SteeringAnchorSnapshot, anchors []*int64) error {
	if _, err := s.Dir(id); err != nil {
		return err
	}
	if len(anchors) != len(want.Turn.Steering) {
		return fmt.Errorf("steering anchor count differs from snapshot")
	}
	for _, anchor := range anchors {
		if anchor != nil && *anchor < 0 {
			return fmt.Errorf("negative steering source offset")
		}
	}
	raw, current, err := s.preparedTurn(ctx, conn, id, want)
	if err != nil {
		return err
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(record["steering"], &entries); err != nil {
		if len(current.Steering) == 0 {
			return nil
		}
		return err
	}
	objects := make([]map[string]json.RawMessage, len(entries))
	for i, entry := range entries {
		if err := json.Unmarshal(entry, &objects[i]); err != nil {
			return fmt.Errorf("steering entry %d is not an object: %w", i, err)
		}
		if objects[i] == nil {
			return fmt.Errorf("steering entry %d is not an object", i)
		}
	}
	changed := false
	for i, steer := range current.Steering {
		if steer.SourceOffset != nil || anchors[i] == nil || (!current.Done() && !steer.Delivered) {
			continue
		}
		fields := objects[i]
		offset, err := json.Marshal(*anchors[i])
		if err != nil {
			return err
		}
		fields["sourceOffset"] = offset
		entries[i], err = json.Marshal(fields)
		if err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	record["steering"], err = json.Marshal(entries)
	if err != nil {
		return err
	}
	patched, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = saveTurnConn(ctx, conn, id, current.N, patched)
	return err
}

// preparedTurn is the shared revision and content fence for prepared transcript
// work. Comparing the content also rejects a reused revision with changed ranges.
func (s *Store) preparedTurn(ctx context.Context, conn *sql.Conn, id string, want SteeringAnchorSnapshot) ([]byte, Turn, error) {
	raw, revision, err := storedTurn(ctx, conn, id, want.Turn.N)
	if err != nil {
		return nil, Turn{}, err
	}
	resolved, err := resolveRowPrompts(ctx, conn, id, want.Turn.N, raw)
	if err != nil {
		return nil, Turn{}, err
	}
	current, err := s.decodeTurnRecord(id, want.Turn.N, resolved)
	if err != nil {
		return nil, Turn{}, err
	}
	if revision != want.Revision || !reflect.DeepEqual(current, want.Turn) {
		return nil, Turn{}, ErrSteeringChanged
	}
	return resolved, current, nil
}
