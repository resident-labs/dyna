package subagent

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/workeroutput"
	"dyna-agent/internal/workeroutput/projection"
)

const sessionOutputProjectionKey = "subagent.output_projection"

// capturedTurn is a captured turn row. The commit refuses to record a
// projection when any of these rows changed since preparation.
type capturedTurn struct {
	n        int
	revision int64
	turn     Turn
}

// TODO: legacy/shim remove later — converts retained session output to the
// current event shapes once. Packs are prepared outside the SQL writer; turn
// revisions fence the one commit. Original artifacts never change.
func (s *Store) migrateOutputProjection() error {
	db, err := s.turnsDB()
	if err != nil {
		return err
	}
	// Probe through the version gate without consuming its version.
	prepare := errors.New("output projection preparation required")
	err = database.Migrate(db, sessionOutputProjectionKey, 1, func(*sql.Conn, int) error { return prepare })
	if !errors.Is(err, prepare) {
		return err
	}
	if err := projection.Migrate(filepath.Dir(s.root())); err != nil {
		return err
	}
	ids, err := db.Query("SELECT DISTINCT session FROM session_turns ORDER BY session")
	if err != nil {
		return err
	}
	var sessions []string
	for ids.Next() {
		var id string
		if err := ids.Scan(&id); err != nil {
			ids.Close()
			return err
		}
		sessions = append(sessions, id)
	}
	if err := errors.Join(ids.Err(), ids.Close()); err != nil {
		return err
	}
	ctx := context.Background()
	for _, id := range sessions {
		key := filepath.ToSlash(filepath.Join("sessions", id, "agents", "1"))
		converted, err := projection.Converted(ctx, db, key)
		if err != nil {
			return err
		}
		if converted {
			continue
		}
		fence, err := captureTurns(ctx, db, id)
		if err != nil {
			return err
		}
		prepared, exists, err := s.prepareSessionOutput(id, key, fence)
		if err != nil {
			return fmt.Errorf("session %s output projection: %w", id, err)
		}
		if !exists {
			continue
		}
		commit := state.For(db).Write(ctx, sessionlife.Changed(id), func(conn *sql.Conn) error {
			now, err := captureTurns(ctx, conn, id)
			if err != nil {
				return err
			}
			if !slices.EqualFunc(fence, now, func(a, b capturedTurn) bool { return a.n == b.n && a.revision == b.revision }) {
				return fmt.Errorf("session %s turns changed during output projection", id)
			}
			return prepared.Commit(ctx, conn)
		})
		if err := errors.Join(commit, prepared.Close()); err != nil {
			return err
		}
	}
	return database.Migrate(db, sessionOutputProjectionKey, 1, func(*sql.Conn, int) error { return nil })
}

type turnQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func captureTurns(ctx context.Context, q turnQuerier, id string) ([]capturedTurn, error) {
	rows, err := q.QueryContext(ctx, "SELECT n, revision, turn FROM session_turns WHERE session=? ORDER BY n", id)
	if err != nil {
		return nil, err
	}
	var out []capturedTurn
	for rows.Next() {
		var t capturedTurn
		var raw []byte
		if err := rows.Scan(&t.n, &t.revision, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &t.turn); err != nil {
			rows.Close()
			return nil, fmt.Errorf("turn %d: %w", t.n, err)
		}
		out = append(out, t)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

func (s *Store) prepareSessionOutput(id, key string, turns []capturedTurn) (projection.Prepared, bool, error) {
	path, err := s.NormalizedPath(id)
	if err != nil {
		return projection.Prepared{}, false, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return projection.Prepared{}, false, nil
	}
	if err != nil {
		return projection.Prepared{}, false, err
	}
	if !info.Mode().IsRegular() {
		return projection.Prepared{}, false, errors.New("normalized output is not a regular file")
	}
	limit := info.Size()
	pack, err := projection.NewPack(key, path)
	if err != nil {
		return projection.Prepared{}, false, err
	}
	defer pack.Abort()
	// The captured prefix is the last complete line of the whole artifact,
	// including lines outside every stored turn range.
	var prefixEnd int64
	if limit > 0 {
		prefixEnd, err = runstore.VisitNormalizedOutputLines(path, 0, limit, func(projection.Line) error { return nil })
		if err != nil {
			return projection.Prepared{}, false, err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return projection.Prepared{}, false, err
	}
	defer f.Close()
	// The pack takes units in start order; overlapping turn ranges are
	// checked by the pack against the lines it already converted.
	units := slices.SortedStableFunc(slices.Values(turns), func(a, b capturedTurn) int {
		return cmp.Compare(a.turn.NormStart, b.turn.NormStart)
	})
	for _, unit := range units {
		start, end := unit.turn.NormStart, unit.turn.NormEnd
		if end == 0 {
			end = limit
		}
		if start < 0 || end < start || end > limit {
			return projection.Prepared{}, false, fmt.Errorf("turn %d output range is outside the captured artifact", unit.n)
		}
		if start == end {
			continue
		}
		var lines []projection.Line
		_, err := runstore.VisitNormalizedOutputLines(path, start, end-start, func(line projection.Line) error {
			if line.Raw == nil {
				// An oversized line keeps its source bytes in the pack.
				line.Raw = make([]byte, line.End-line.Start)
				if _, err := f.ReadAt(line.Raw, line.Start); err != nil {
					return err
				}
			}
			line.Raw = bytes.Clone(line.Raw)
			lines = append(lines, line)
			return nil
		})
		if err != nil {
			return projection.Prepared{}, false, err
		}
		dialect := unit.turn.OutputDialect
		if err := pack.Add(lines, func(events []workeroutput.Event) []int {
			return workeroutput.ConvertSessionTurn(dialect, events)
		}); err != nil {
			return projection.Prepared{}, false, fmt.Errorf("artifact %s turn %d range [%d,%d): %w", key, unit.n, start, end, err)
		}
	}
	prepared, err := pack.Finish(limit, prefixEnd)
	return prepared, err == nil, err
}
