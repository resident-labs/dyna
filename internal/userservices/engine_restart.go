package userservices

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

const engineRestartWorker = "dyna-engine-restart.service"

const engineRestartDelay = 2 * time.Hour

// VoiceQuietNow also protects calls that are starting or stopping. An active
// engine that cannot answer has unknown activity, which counts as busy.
func VoiceQuietNow(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandBound)
	defer cancel()
	var status struct {
		Live   bool   `json:"live"`
		RoomID string `json:"roomId"`
	}
	err := engineclient.New("", engineclient.RestartWait(0)).Call(ctx, "VoiceStatus", nil, &status)
	if err == nil {
		if status.Live || status.RoomID != "" {
			return "a voice call is live", nil
		}
		return "", nil
	}
	out, queryErr := systemctlUser(ctx, os.Environ(), "show", Engine, "--property=ActiveState")
	if queryErr == nil && (property(out, "ActiveState") == "inactive" || property(out, "ActiveState") == "failed") {
		return "", nil
	}
	return "voice call activity is unknown: " + err.Error(), nil
}

func engineRestartKey(dir string) string {
	return "userservices.engine-restart:" + filepath.Clean(dir)
}

func engineRestartLock(dir string) string {
	return filepath.Join(dir, "dyna-engine.restart-lock")
}

// The existing meta table lets the newly installed binary record a request
// before the running engine has this code. Repeated installs keep its deadline.
func engineRestartDeadline(ctx context.Context, dir string, create time.Time) (time.Time, error) {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return time.Time{}, err
	}
	key := engineRestartKey(dir)
	if !create.IsZero() {
		err = state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
			_, err := conn.ExecContext(ctx, "INSERT OR IGNORE INTO meta(key,value) VALUES(?,?)", key, strconv.FormatInt(create.UnixNano(), 10))
			return err
		})
		if err != nil {
			return time.Time{}, err
		}
	}
	var value string
	err = db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	ns, err := strconv.ParseInt(value, 10, 64)
	return time.Unix(0, ns), err
}

func clearEngineRestart(ctx context.Context, dir string) error {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM meta WHERE key=?", engineRestartKey(dir))
		return err
	})
}

// EngineRestarted acknowledges requests made before this engine started. The
// writable engine calls it after winning its private socket, before serving.
// New requests made during startup still need a later restart.
func EngineRestarted(ctx context.Context, started time.Time) error {
	db, err := database.Open(store.DataDir())
	if err != nil {
		return err
	}
	return state.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM meta WHERE key LIKE 'userservices.engine-restart:%' AND CAST(value AS INTEGER)<=?", started.Add(engineRestartDelay).UnixNano())
		return err
	})
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func deferEngineRestart(ctx context.Context, o Options) (bool, error) {
	if o.EngineQuiet == nil {
		return false, nil
	}
	busy, err := o.EngineQuiet(ctx)
	if err != nil || busy == "" {
		return false, err
	}
	deadline, err := engineRestartDeadline(ctx, o.Dir, o.now().Add(engineRestartDelay))
	if err != nil || !o.now().Before(deadline) {
		return false, err
	}
	fmt.Fprintf(o.Output, "Deferred engine restart: %s\n", busy)
	return true, nil
}

// WaitEngineRestart runs in its own service so even the first update can wait
// for a call to end. Its durable deadline survives newer installs and retries.
func WaitEngineRestart(ctx context.Context, o Options) error {
	if o.Run == nil && testing.Testing() {
		return fmt.Errorf("refusing engine restart from Go tests; use a fake runner")
	}
	if o.Dir == "" {
		o.Dir = Dir()
	}
	for {
		unlock, err := store.LockContext(ctx, engineRestartLock(o.Dir))
		if err != nil {
			return err
		}
		done, err := engineRestartStep(ctx, o)
		unlock()
		if err != nil || done {
			return err
		}
		if err := o.pause(ctx, time.Second); err != nil {
			return err
		}
	}
}

func engineRestartStep(ctx context.Context, o Options) (bool, error) {
	deadline, err := engineRestartDeadline(ctx, o.Dir, time.Time{})
	if err != nil || deadline.IsZero() {
		return true, err
	}
	if o.now().Before(deadline) && o.EngineQuiet != nil {
		busy, err := o.EngineQuiet(ctx)
		if err != nil || busy != "" && o.now().Before(deadline) {
			return false, err
		}
	}
	run := o.Run
	if run == nil {
		run = systemctlRunner(os.Environ())
	}
	restartCtx, cancel := context.WithTimeout(ctx, restartBound)
	defer cancel()
	out, err := run(restartCtx, "systemctl", "--user", "restart", Engine)
	if err != nil {
		return true, fmt.Errorf("restart %s: %w: %s", Engine, err, out)
	}
	return true, clearEngineRestart(ctx, o.Dir)
}
