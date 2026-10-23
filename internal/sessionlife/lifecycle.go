// Package sessionlife supplies short publication guards without importing the
// session manager or running repair/report callbacks inside another store lock.
package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"

	"dyna-agent/internal/state"
)

var sessionID = regexp.MustCompile(`^ses_[0-9]{8}-[0-9]{6}-[a-z0-9]+$`)

type Closing struct {
	Token string    `json:"token"`
	PID   int       `json:"pid"`
	At    time.Time `json:"at,omitzero"`
}

// ClosingLimit bounds a dormant-close check. Checks take seconds; a
// reservation older than this was left by a holder that died or hung, even
// if its process ID now names another process.
const ClosingLimit = 10 * time.Minute

// Active reports a reservation that still blocks the session. A holder killed
// before its release, such as a dormancy check whose turn ended, must not
// wedge the session for good.
func (c *Closing) Active() bool {
	if c == nil {
		return false
	}
	if c.PID > 0 && errors.Is(syscall.Kill(c.PID, 0), syscall.ESRCH) {
		return false
	}
	return c.At.IsZero() || time.Since(c.At) < ClosingLimit
}

var ErrClosed = errors.New("session is closed")

func Check(sessions, id string) error {
	if !sessionID.MatchString(id) {
		return nil
	}
	db, ok, err := readable(sessions)
	if err != nil {
		return fmt.Errorf("cannot verify session ownership: %w", err)
	}
	if !ok {
		return check(legacyOnly{}, sessions, id)
	}
	return check(db, sessions, id)
}

// CheckTx is Check inside the caller's dyna.db write transaction on conn. It
// takes no file lock, so it is safe inside any transaction.
func CheckTx(conn *sql.Conn, sessions, id string) error {
	if !sessionID.MatchString(id) {
		return nil
	}
	if err := ensureTx(conn); err != nil {
		return err
	}
	return check(conn, sessions, id)
}

// ensureTx creates the record table on the caller's write transaction when
// it is missing; records() would wait for the writer that transaction holds.
func ensureTx(conn *sql.Conn) error {
	_, err := conn.ExecContext(context.Background(), recordSchema)
	return err
}

func check(q querier, sessions, id string) error {
	raw, _, err := loadRaw(q, sessions, id)
	var m Snapshot
	if err == nil {
		err = decode(id, raw, &m)
	}
	if err != nil {
		return fmt.Errorf("cannot verify session ownership: %w", err)
	}
	if m.ID != id {
		return errors.New("cannot verify session ownership: stored session ID does not match")
	}
	if m.HistoryOnly {
		return errors.New("historical session cannot publish")
	}
	if m.IsClosed() {
		return ErrClosed
	}
	if m.Closing.Active() {
		return errors.New("session is being checked for dormant close; retry after the check")
	}
	return nil
}

// WithPublication serializes a store publication with the dormant-clear
// reread. Callers must not acquire it while holding session lifecycle locks.
func WithPublication(ctx context.Context, root, name string, fn func() error) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// Activate checks a work owner and clears dormancy before publication. Call it
// under the work store's publication lock, never under a session lock, and
// never inside a dyna.db transaction (use WithSessionLocks and ActivateTx).
func Activate(sessions, id string) error {
	if !sessionID.MatchString(id) {
		return nil
	}
	return WithSessionLocks(sessions, []string{id}, func() error {
		dir := filepath.Join(sessions, id)
		// Missing owners must not create empty session directories.
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("cannot verify session ownership: %w", err)
		}
		db, err := records(sessions)
		if err != nil {
			return err
		}
		return state.For(db).Write(context.Background(), Changed(id), func(conn *sql.Conn) error { return ActivateTx(conn, sessions, id) })
	})
}

// WithSessionLocks runs fn holding the publication lock (session.lock) of
// each named session that has a folder, taken in name order. Take these file
// locks before opening a dyna.db transaction, never inside one. It also
// prepares the record table, which a transaction cannot do for itself.
func WithSessionLocks(sessions string, ids []string, fn func() error) error {
	if _, err := records(sessions); err != nil {
		return err
	}
	var locks []string
	for _, id := range ids {
		if sessionID.MatchString(id) && !slices.Contains(locks, id) {
			if _, err := os.Stat(filepath.Join(sessions, id)); err == nil {
				locks = append(locks, id)
			}
		}
	}
	slices.Sort(locks)
	var take func(int) error
	take = func(i int) error {
		if i == len(locks) {
			return fn()
		}
		beforeSessionLock(locks[i])
		return WithPublication(context.Background(), filepath.Join(sessions, locks[i]), "session.lock", func() error { return take(i + 1) })
	}
	return take(0)
}

// beforeSessionLock lets a test act just before a session lock is taken.
var beforeSessionLock = func(string) {}

// SetBeforeSessionLock replaces the hook that runs just before
// WithSessionLocks takes a session lock, for tests that must know a caller
// reached that point, and returns a function that restores it.
func SetBeforeSessionLock(f func(id string)) (restore func()) {
	old := beforeSessionLock
	beforeSessionLock = f
	return func() { beforeSessionLock = old }
}

// IsSessionID reports whether id names a session (not "owner" or a room).
func IsSessionID(id string) bool { return sessionID.MatchString(id) }

// ActivateTx is Activate inside the caller's dyna.db write transaction on
// conn, for stores whose work lives in dyna.db (goals). It takes no file
// lock, which would be a lock inside a transaction. The caller holds session
// id's lock from WithSessionLocks, taken before the transaction: a session's
// own metadata updates read, then write under that lock, and a wake between
// the two would be lost. The dormant close also re-checks the record in its
// final write, as a second guard.
func ActivateTx(conn *sql.Conn, sessions, id string) error {
	if !sessionID.MatchString(id) {
		return nil
	}
	if err := ensureTx(conn); err != nil {
		return err
	}
	if err := check(conn, sessions, id); err != nil {
		return err
	}
	raw, _, err := loadRaw(conn, sessions, id)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := decode(id, raw, &fields); err != nil {
		return err
	}
	var status string
	_ = json.Unmarshal(fields["status"], &status)
	if status != "dormant" && len(fields["finished"]) == 0 {
		return nil
	}
	fields["dormantWake"] = json.RawMessage("true")
	fields["status"] = json.RawMessage(`"idle"`)
	delete(fields, "finished")
	return SaveTx(conn, id, fields)
}
