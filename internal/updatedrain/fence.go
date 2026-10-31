// Package updatedrain serializes update restarts with new CI work.
package updatedrain

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/procident"
	"dyna-agent/internal/state"
)

var ErrDraining = errors.New("engine update is draining CI work")

const schema = `CREATE TABLE IF NOT EXISTS update_drain (id INTEGER PRIMARY KEY CHECK(id=1), pid INTEGER NOT NULL, started INTEGER NOT NULL, expires INTEGER NOT NULL)`

// Active must run inside the same writer transaction as the admission.
// A crashed installer cannot strand admissions: PID reuse is checked too.
func Active(ctx context.Context, conn *sql.Conn) (bool, error) {
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return false, err
	}
	var pid int
	var started uint64
	var expires int64
	err := conn.QueryRowContext(ctx, `SELECT pid,started,expires FROM update_drain WHERE id=1`).Scan(&pid, &started, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if pid <= 0 || !procident.Alive(pid) {
		return time.Now().Unix() < expires, nil
	}
	actual, err := procident.StartTime(pid)
	if err != nil {
		return true, nil
	} // Fail closed while the owner may live.
	if actual != started {
		return time.Now().Unix() < expires, nil
	}
	return true, nil
}

// Acquire starts a drain, replacing only a dead installer's fence.
func Acquire(ctx context.Context, db *sql.DB) (func() error, error) {
	return acquire(ctx, db, false)
}

// AcquireExisting takes over an unexpired abandoned drain before a retry builds.
// A nil release means there was no drain to take over.
func AcquireExisting(ctx context.Context, db *sql.DB) (func() error, error) {
	return acquire(ctx, db, true)
}

func acquire(ctx context.Context, db *sql.DB, existingOnly bool) (func() error, error) {
	pid := os.Getpid()
	started, err := procident.StartTime(pid)
	if err != nil {
		return nil, err
	}
	acquired := false
	err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, schema); err != nil {
			return err
		}
		var oldPID int
		var oldStart uint64
		var expires int64
		err := conn.QueryRowContext(ctx, `SELECT pid,started,expires FROM update_drain WHERE id=1`).Scan(&oldPID, &oldStart, &expires)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existingOnly && (errors.Is(err, sql.ErrNoRows) || time.Now().Unix() >= expires) {
			return nil
		}
		if err == nil && oldPID > 0 && procident.Alive(oldPID) {
			actual, err := procident.StartTime(oldPID)
			if err != nil || actual == oldStart {
				return ErrDraining
			}
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO update_drain VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET pid=excluded.pid,started=excluded.started,expires=excluded.expires`, pid, started, time.Now().Add(30*time.Minute).Unix())
		acquired = err == nil
		return err
	})
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, nil
	}
	return func() error {
		return state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(context.Background(), `DELETE FROM update_drain WHERE id=1 AND pid=? AND started=?`, pid, started)
			return err
		})
	}, nil
}

// Refresh keeps the fence across an installer killed by its deadline. Another
// installer can take over immediately. If retries stop, it expires after 30 minutes.
func Refresh(ctx context.Context, db *sql.DB) error {
	pid := os.Getpid()
	started, err := procident.StartTime(pid)
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE update_drain SET expires=? WHERE id=1 AND pid=? AND started=?`, time.Now().Add(30*time.Minute).Unix(), pid, started)
		return err
	})
}

// ClearAbandoned removes only a lease whose installer is gone. A live
// installer must retain its fence through publication even if Updates is off.
func ClearAbandoned(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return err
	}
	var pid int
	var started uint64
	err := conn.QueryRowContext(ctx, `SELECT pid,started FROM update_drain WHERE id=1`).Scan(&pid, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if pid > 0 && procident.Alive(pid) {
		actual, err := procident.StartTime(pid)
		if err != nil || actual == started {
			return nil
		}
	}
	_, err = conn.ExecContext(ctx, `DELETE FROM update_drain WHERE id=1 AND pid=? AND started=?`, pid, started)
	return err
}

// Suspend hands the lease to a retry in the same transaction that checks
// whether Updates is enabled. Disabling can then clear the ownerless lease.
func Suspend(ctx context.Context, db *sql.DB) error {
	pid := os.Getpid()
	started, err := procident.StartTime(pid)
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var enabled bool
		if err := conn.QueryRowContext(ctx, `SELECT enabled FROM updater_service WHERE id=1`).Scan(&enabled); err != nil {
			return err
		}
		if enabled {
			_, err = conn.ExecContext(ctx, `UPDATE update_drain SET pid=0,started=0,expires=? WHERE id=1 AND pid=? AND started=?`, time.Now().Add(30*time.Minute).Unix(), pid, started)
		} else {
			_, err = conn.ExecContext(ctx, `DELETE FROM update_drain WHERE id=1 AND pid=? AND started=?`, pid, started)
		}
		return err
	})
}
