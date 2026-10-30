//go:build linux

package shells

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// TODO: legacy/shim remove later — sweeps directories left by old row-only
// pruning. Keep this until the upgrade floor guarantees the sweep has run.
// SweepOrphanDirectories processes one bounded directory page per pass. It
// starts another scan at EOF to revisit entries that were too young earlier.
func (s *Store) SweepOrphanDirectories(ctx context.Context, now time.Time) (int, error) {
	db, err := s.db()
	if err != nil {
		return 0, err
	}
	removed := 0
	err = sessionlife.WithPublication(ctx, s.Root, "publication.lock", func() error {
		const key = "shells.orphan_sweep.cursor"
		var cursor string
		err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&cursor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var offset int64
		if cursor != "" {
			offset, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil {
				return err
			}
		}
		f, err := os.Open(s.Root)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
		buf := make([]byte, 32*1024)
		n, err := syscall.ReadDirent(int(f.Fd()), buf)
		if err != nil {
			return err
		}
		next := "0"
		if n > 0 {
			position, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			next = strconv.FormatInt(position, 10)
			_, _, names := syscall.ParseDirent(buf[:n], -1, nil)
			deadline := time.Now().Add(5 * time.Second)
			for _, id := range names {
				// Retry the same page next pass if filesystem work takes too long.
				if time.Now().After(deadline) {
					return nil
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !ValidID(id) {
					continue
				}
				dir := filepath.Join(s.Root, id)
				info, err := os.Lstat(dir)
				if err != nil || !info.IsDir() || now.Sub(info.ModTime()) < 24*time.Hour {
					continue
				}
				marker, err := os.Lstat(filepath.Join(dir, exitedMarker))
				if err != nil || !marker.Mode().IsRegular() || now.Sub(marker.ModTime()) < 24*time.Hour {
					continue
				}
				var exists int
				err = db.QueryRowContext(ctx, `SELECT 1 FROM shell_jobs WHERE id=?`, id).Scan(&exists)
				if err == nil {
					continue
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
				removed++
			}
		}
		return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, next)
			return err
		})
	})
	return removed, err
}
