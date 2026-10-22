package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

// RequestKeep is how long the engine remembers an applied request ID
// (requirements 9 and 28).
const RequestKeep = 24 * time.Hour

// ErrRepeated is a write whose request ID was already applied. The write
// did nothing and published nothing.
var ErrRepeated = errors.New("this request was already applied")

// ErrRequestConflict refuses reuse of an applied ID for a different call.
var ErrRequestConflict = errors.New("request ID was reused with different method or arguments")

const requestSchema = `CREATE TABLE request_ids (
 id TEXT PRIMARY KEY,
 method TEXT NOT NULL DEFAULT '',
 result BLOB,
 fingerprint BLOB,
 done_at INTEGER NOT NULL
);
CREATE INDEX request_ids_done ON request_ids(done_at);`

// MigrateRequests brings the request ID table in dataDir's dyna.db to this
// version. Only the engine calls it, at start (requirement 41).
func MigrateRequests(dataDir string) error {
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	return database.Migrate(db, "state.requests", 2, func(conn *sql.Conn, old int) error {
		if old == 0 {
			_, err := conn.ExecContext(context.Background(), requestSchema)
			return err
		}
		_, err := conn.ExecContext(context.Background(), "ALTER TABLE request_ids ADD COLUMN fingerprint BLOB")
		return err
	})
}

// request is the request ID of the call that ctx belongs to. Its first SQL
// write records the ID in the same transaction, so a retry of a write that
// committed is refused with ErrRepeated, and never applied twice.
type request struct {
	id          string
	method      string
	fingerprint []byte
	recorded    atomic.Bool
}

type requestKey struct{}

// ForRequest returns ctx for a call that carries request ID id
// (requirement 9). An empty id carries none.
func ForRequest(ctx context.Context, id string) context.Context {
	return ForRequestCall(ctx, id, "", nil)
}

// ForRequestCall binds an RPC's method and canonical arguments fingerprint to
// its first mutation receipt and every later result write.
func ForRequestCall(ctx context.Context, id, method string, fingerprint []byte) context.Context {
	if id == "" {
		return withoutRequest(ctx)
	}
	return context.WithValue(ctx, requestKey{}, &request{id: id, method: method, fingerprint: bytes.Clone(fingerprint)})
}

func withoutRequest(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestKey{}, (*request)(nil))
}

func requestOf(ctx context.Context) *request {
	req, _ := ctx.Value(requestKey{}).(*request)
	return req
}

// recordFirst wraps fn so that the call's first write records its request
// ID first. It returns nil when there is nothing to record.
func (w *Writer) recordFirst(ctx context.Context, fn func(*sql.Conn) error) (func(*sql.Conn) error, *request) {
	req := requestOf(ctx)
	if req == nil || req.recorded.Load() {
		return fn, nil
	}
	return func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, "INSERT INTO request_ids(id,method,fingerprint,done_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", req.id, req.method, req.fingerprint, w.now().UnixMilli())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var method string
			var fingerprint []byte
			if err := conn.QueryRowContext(ctx, "SELECT method,fingerprint FROM request_ids WHERE id=?", req.id).Scan(&method, &fingerprint); err != nil {
				return err
			}
			if err := compareRequestFingerprint(method, fingerprint, req.method, req.fingerprint); err != nil {
				return err
			}
			return ErrRepeated
		}
		return fn(conn)
	}, req
}

// RequestDone reports whether request ID id was applied, and its stored
// result. A request whose write committed but whose result was not stored
// yet (the engine stopped in between) is done with a nil result.
func (w *Writer) RequestDone(ctx context.Context, id string) (result []byte, done bool, err error) {
	var method string
	var fingerprint []byte
	err = w.db.QueryRowContext(ctx, "SELECT method,fingerprint,result FROM request_ids WHERE id=?", id).Scan(&method, &fingerprint, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if req := requestOf(ctx); req != nil {
		if req.id != id {
			return nil, true, ErrRequestConflict
		}
		if err := compareRequestFingerprint(method, fingerprint, req.method, req.fingerprint); err != nil {
			return nil, true, err
		}
	}
	return result, true, nil
}

// compareRequestFingerprint is shared by cached replies, mutation receipts,
// and both result finish paths.
func compareRequestFingerprint(storedMethod string, stored []byte, method string, fingerprint []byte) error {
	// TODO: legacy/shim remove later — keeps pre-fingerprint request receipts replayable until RequestKeep pruning removes them; remove after the upgrade's 24-hour retention window.
	if len(stored) == 0 {
		return nil
	}
	if method != storedMethod || !bytes.Equal(stored, fingerprint) {
		return ErrRequestConflict
	}
	return nil
}

// FinishRequest stores the result of applied request id. It also records a
// request that made no SQL write (only file or log writes), so its retry is
// answered from here too.
func (w *Writer) FinishRequest(ctx context.Context, id, method string, result []byte) error {
	return w.Write(withoutRequest(ctx), hub.Event{}, func(conn *sql.Conn) error {
		return w.finishRequest(ctx, conn, id, method, result)
	})
}

// FinishRequestIn saves the current ForRequest result on the connection supplied
// to a Writer.Write callback. The mutation, request ID, and opaque result bytes
// commit or roll back together. Without a request ID it does nothing.
// The result must match the JSON response returned by the method: the engine
// calls FinishRequest afterward and replaces these bytes with that response.
func (w *Writer) FinishRequestIn(ctx context.Context, conn *sql.Conn, method string, result []byte) error {
	req := requestOf(ctx)
	if req == nil {
		return nil
	}
	return w.finishRequest(ctx, conn, req.id, method, result)
}

func (w *Writer) finishRequest(ctx context.Context, conn *sql.Conn, id, method string, result []byte) error {
	var fingerprint []byte
	if req := requestOf(ctx); req != nil {
		if req.id != id || req.method != "" && req.method != method {
			return ErrRequestConflict
		}
		fingerprint = req.fingerprint
	}
	var storedMethod string
	var stored []byte
	err := conn.QueryRowContext(ctx, "SELECT method,fingerprint FROM request_ids WHERE id=?", id).Scan(&storedMethod, &stored)
	if err == nil {
		if err := compareRequestFingerprint(storedMethod, stored, method, fingerprint); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO request_ids(id,method,result,fingerprint,done_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET method=excluded.method,result=excluded.result", id, method, result, fingerprint, w.now().UnixMilli())
	return err
}

// PruneRequests forgets at most 128 request IDs applied strictly before
// cutoff (requirement 28). An empty indexed check never enters the writer.
func (w *Writer) PruneRequests(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var expired bool
	if err := w.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM request_ids WHERE done_at < ?)", cutoff.UnixMilli()).Scan(&expired); err != nil {
		return 0, err
	}
	if !expired {
		return 0, nil
	}
	var n int64
	err := w.Write(withoutRequest(ctx), hub.Event{}, func(conn *sql.Conn) error {
		// Choose the batch inside the transaction: the earlier read only
		// checks for work and cannot authorize deletion of a newer row.
		res, err := conn.ExecContext(ctx, `DELETE FROM request_ids WHERE id IN (
SELECT id FROM request_ids WHERE done_at < ? ORDER BY done_at LIMIT 128
)`, cutoff.UnixMilli())
		if err == nil {
			n, err = res.RowsAffected()
		}
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}
