package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"dyna-agent/internal/hub"
	appstate "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

// Topic names the event that a committed PR state write publishes. The engine
// turns it into room PR rows; clients never see it.
const Topic = "prstate"

type diskRecord struct {
	Record    *Record                   `json:"record"`
	Responses map[string]CachedResponse `json:"responses,omitempty"`
}

// Update runs fn on a private copy of the store under LockPath(root) and
// commits its changes. The copy becomes the shared snapshot without a
// second clone, because only Update holds it.
func Update(ctx context.Context, root string, fn func(*State) error) error {
	return update(ctx, root, func(snapshot Snapshot) (State, error) {
		st := snapshot.Copy()
		if err := fn(&st); err != nil {
			return State{}, err
		}
		return st, nil
	})
}

// update shares the PR lock, committed read, and writer fence for both callback
// contracts. Only the private prepared state reaches write.
//
// Lock order: the PR lock (LockPath), then the snapshot mutex, then the
// state writer. Code that holds the writer never takes the other two: a
// Write callback runs only SQL, and state.Writer publishes events after it
// releases the writer, so an event handler may read PR state (issue #247).
func update(ctx context.Context, root string, fn func(Snapshot) (State, error)) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(LockPath(root), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// TODO: legacy/shim remove later — CLI processes still write PR state in process, so writers share this cross-process lock; remove when those writes become engine calls (item 14).
	if err = store.FlockContext(ctx, f); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	snapshot, err := readSnapshot(root)
	if err != nil {
		return err
	}
	st, err := fn(snapshot)
	if err != nil {
		return err
	}
	return write(root, st)
}

// Write commits the changes from the last read in one transaction and stores
// only the rows that differ. The caller must hold LockPath(root).
func Write(root string, st State) error {
	return write(root, cloneState(st))
}

// write is Write for a state that no caller uses afterward.
func write(root string, st State) (err error) {
	if st.Version != 1 {
		return fmt.Errorf("unsupported PR state version %d", st.Version)
	}
	normalizeState(&st)
	// Closed and merged records leave after 14 days. Each one is logged once,
	// after the write that removes it commits.
	cutoff := time.Now().Add(-14 * 24 * time.Hour)
	var pruned []string
	for key, r := range st.Records {
		if r != nil && (r.State == "closed" || r.State == "merged") && !r.ClosedAt.IsZero() && r.ClosedAt.Before(cutoff) {
			pruned = append(pruned, fmt.Sprintf("PR state: pruned %s record %s (#%d), closed %s", r.State, key, r.Number, r.ClosedAt.Format(time.DateOnly)))
			delete(st.Records, key)
			delete(st.Responses, key)
		}
	}
	defer func() {
		if err == nil {
			for _, line := range pruned {
				log.Print(line)
			}
		}
	}()
	// Standalone writers leave the lock file in place for shared readers.
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(LockPath(root), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	db, err := open(root)
	if err != nil {
		return err
	}
	snapshot.Lock()
	defer snapshot.Unlock()
	writer := appstate.For(db)
	// Refresh and encode on a read snapshot, before reserving the shared writer.
	// The PR lock and snapshot lock still serialize PR writers and readers.
	if err := writer.Read(context.Background(), func(r appstate.Reader) error { return refresh(r, root) }); err != nil {
		return err
	}
	before := snapshot.revision
	revision := before + 1
	rows, err := prepareRows(snapshot.state, snapshot.index, st)
	if err != nil {
		return err
	}
	err = writer.Write(context.Background(), hub.Event{Topic: Topic}, func(conn *sql.Conn) error {
		// Refuse a stale preparation rather than overwrite an intervening write.
		var current int64
		err := conn.QueryRowContext(context.Background(), "SELECT revision FROM pr_state WHERE id=1").Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if current != before {
			return fmt.Errorf("PR state changed while preparing a write: revision %d became %d", before, current)
		}
		if err := rows.apply(conn, revision); err != nil {
			return err
		}
		if len(pruned) == 0 {
			return nil
		}
		// Maintenance reports this count with its own removals (TakePruned).
		_, err = conn.ExecContext(context.Background(), `INSERT INTO meta(key,value) VALUES(?,?)
ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+CAST(excluded.value AS INTEGER)`, prunedKey, fmt.Sprint(len(pruned)))
		return err
	})
	// Only a committed write installs the prepared snapshot. A failed write
	// publishes nothing; the next read refreshes from the durable revision.
	if err == nil && rows.index != nil {
		// Reuse immutable rows that preparation proved unchanged. Quota is derived
		// from metadata and can change without a stored row write.
		changed := make(map[string]bool, len(rows.upserts))
		for _, row := range rows.upserts {
			changed[row.key] = true
		}
		for key, record := range st.Records {
			if old := snapshot.state.Records[key]; old != nil && !changed[key] && reflect.DeepEqual(record.RateLimit, old.RateLimit) {
				st.Records[key] = old
			}
		}
		snapshot.revision, snapshot.index, snapshot.state = revision, rows.index, st
	}
	return err
}

// preparedRows contains only the changed SQL values. Comparison and encoding
// finish before the shared writer is reserved.
type preparedRows struct {
	index   []byte
	upserts []preparedRecord
	deletes []string
}

type preparedRecord struct {
	key, room, repo string
	number          int
	closed          bool
	body            []byte
}

// marshalStored is the persistent JSON encoder. Tests can hold preparation.
var marshalStored = json.Marshal

func prepareRows(old State, oldIndex []byte, st State) (preparedRows, error) {
	var rows preparedRows
	metadata := st
	metadata.Records, metadata.Responses = nil, nil
	index, err := marshalStored(metadata)
	if err != nil {
		return rows, err
	}
	changed := !bytes.Equal(index, oldIndex)
	for key, r := range st.Records {
		if old.Records[key] != nil && sameStored(old.Records[key], r) && reflect.DeepEqual(old.Responses[key], st.Responses[key]) {
			continue
		}
		body, err := marshalStored(diskRecord{r, st.Responses[key]})
		if err != nil {
			return rows, err
		}
		row := preparedRecord{key: key, body: body}
		if r != nil {
			row.room, row.repo, row.number = r.RoomID, r.Repo, r.Number
			row.closed = r.State == "closed" || r.State == "merged"
		}
		rows.upserts = append(rows.upserts, row)
	}
	for key := range old.Records {
		if _, kept := st.Records[key]; !kept {
			rows.deletes = append(rows.deletes, key)
		}
	}
	if changed || len(rows.upserts) != 0 || len(rows.deletes) != 0 {
		rows.index = index
	}
	return rows, nil
}

func (rows preparedRows) apply(conn *sql.Conn, revision int64) error {
	ctx := context.Background()
	for _, r := range rows.upserts {
		if _, err := conn.ExecContext(ctx, `INSERT INTO pr_records(key,room,repo,number,closed,rev,body) VALUES(?,?,?,?,?,?,?)
ON CONFLICT(key) DO UPDATE SET room=excluded.room, repo=excluded.repo, number=excluded.number, closed=excluded.closed, rev=excluded.rev, body=excluded.body`,
			r.key, r.room, r.repo, r.number, r.closed, revision, r.body); err != nil {
			return err
		}
	}
	for _, key := range rows.deletes {
		if _, err := conn.ExecContext(ctx, "DELETE FROM pr_records WHERE key=?", key); err != nil {
			return err
		}
	}
	if rows.index == nil {
		return nil
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO pr_state(id,revision,body) VALUES(1,?,?)
ON CONFLICT(id) DO UPDATE SET revision=excluded.revision, body=excluded.body`, revision, rows.index)
	return err
}

// sameStored compares two records as they are stored. The quota display is
// derived from the store's rate limits on every read and write, so a new
// quota alone stores no row.
func sameStored(a, b *Record) bool {
	if b == nil {
		return a == nil
	}
	x, y := *a, *b
	x.RateLimit, y.RateLimit = RateLimit{}, RateLimit{}
	return reflect.DeepEqual(x, y)
}

// MarshalJSON excludes the derived quota display from persisted records.
func (d diskRecord) MarshalJSON() ([]byte, error) {
	type recordFields Record
	var record any
	if d.Record != nil {
		record = struct {
			*recordFields
			RateLimit *RateLimit `json:"rateLimit,omitempty"`
		}{recordFields: (*recordFields)(d.Record)}
	}
	return json.Marshal(struct {
		Record    any                       `json:"record"`
		Responses map[string]CachedResponse `json:"responses,omitempty"`
	}{record, d.Responses})
}

// prunedKey counts closed records Write removed since maintenance last asked.
const prunedKey = "pr.pruned.pending"

// TakePruned returns how many closed records Write removed since the last
// call, and resets the count.
func TakePruned(root string) (int, error) {
	db, err := open(root)
	if err != nil {
		return 0, err
	}
	var n int
	ctx := context.Background()
	err = appstate.For(db).Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		var value string
		err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, prunedKey).Scan(&value)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if n, err = strconv.Atoi(value); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `DELETE FROM meta WHERE key=?`, prunedKey)
		return err
	})
	return n, err
}
