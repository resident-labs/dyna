package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// TODO: legacy/shim remove later — complete eager import before retiring turn files.
func (s *Store) migrateRetainedTurns(conn *sql.Conn) error {
	ctx := context.Background()
	var version int
	err := conn.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='subagent.turns.import'").Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version > 2 {
		return fmt.Errorf("subagent.turns.import is version %d; this dyna knows up to 2", version)
	}
	if version == 2 {
		// Completed import preserves canonical absence, including deleted rows.
		return nil
	}
	if err := s.importRetainedTurns(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('subagent.turns.import','2') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	return err
}

// TODO: legacy/shim remove later — one-time import of retained numbered turn
// artifacts. Session metadata and its turn count do not bound this scan.
func (s *Store) importRetainedTurns(conn *sql.Conn) error {
	root, err := os.OpenRoot(s.root())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, entry := range entries {
		id := entry.Name()
		if !sessionlife.IsSessionID(id) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retained turn folder %s is a symlink", filepath.Join(s.root(), id))
		}
		if !entry.IsDir() {
			continue
		}
		rel := filepath.Join(id, "turns")
		info, err := root.Lstat(rel)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("retained turn folder %s: %w", filepath.Join(s.root(), rel), err)
		}
		if !info.IsDir() {
			return fmt.Errorf("retained turn folder %s is not a directory", filepath.Join(s.root(), rel))
		}
		// Root confines enumeration even if a directory changes meanwhile.
		turns, err := fs.ReadDir(root.FS(), filepath.ToSlash(rel))
		if err != nil {
			return fmt.Errorf("retained turn folder %s: %w", filepath.Join(s.root(), rel), err)
		}
		for _, turn := range turns {
			n, err := strconv.Atoi(strings.TrimSuffix(turn.Name(), ".json"))
			if err != nil || n < 1 || turn.Name() != strconv.Itoa(n)+".json" {
				continue
			}
			// Check canonical ownership before opening any stale source.
			var exists int
			err = conn.QueryRowContext(ctx, "SELECT 1 FROM session_turns WHERE session=? AND n=?", id, n).Scan(&exists)
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			path := filepath.Join(rel, turn.Name())
			_, err = state.ImportMigrationRoot(ctx, conn, sessionlife.TurnMirrorKey(id, n), root, path, func(conn *sql.Conn, raw []byte) error {
				var body struct {
					N int `json:"n"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					return err
				}
				if body.N != n {
					return fmt.Errorf("turn number %d does not match filename %d", body.N, n)
				}
				_, err := saveTurnConn(ctx, conn, id, n, raw)
				return err
			})
			if err != nil {
				return fmt.Errorf("import retained turn %s: %w", filepath.Join(s.root(), path), err)
			}
		}
	}
	return nil
}
