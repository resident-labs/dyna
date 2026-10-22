// Package state is the one write path for Dyna's state. A write either runs
// in one SQLite writer transaction (database.Immediate) or appends one line
// to a JSONL log. Each write publishes its change once, after it commits.
// TestStateWritesGoThroughHelper keeps every other state write on a list
// that may only shrink. It counts the places that open a transaction or a
// connection (database.Immediate, Begin, BeginTx, db.Conn) and SQL run on a
// *sql.DB, not the statements run on a *sql.Conn or *sql.Tx that a function
// receives, so a store's conn helpers need no list line once its writes go
// through Writer.Write.
package state

import (
	"context"
	"database/sql"
	"io"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

// Writer writes state, publishes each change after its commit, and keeps
// the numbers that the engine's Health call reports. For returns the one
// Writer of each database in this process.
// It also provides read snapshots on the shared database handle.
type Writer struct {
	db      *sql.DB
	publish atomic.Pointer[func(hub.Event)]
	// Logf reports slow writes. Nil uses log.Printf.
	Logf func(string, ...any)
	// Now is the clock. Nil uses time.Now.
	Now func() time.Time

	appendMu sync.Mutex // orders appends and their events
	mirrorMu sync.Mutex // orders rollback copies, stamps and imports
	mu       sync.Mutex
	next     uint64
	inFlight map[uint64]time.Time // write -> when it was called
	recent   []done               // finished writes, newest last
}

// writers holds this process's one Writer per database, so every store
// writes, publishes and counts its writes in one place.
var writers = struct {
	sync.Mutex
	byDB map[*sql.DB]*Writer
}{byDB: map[*sql.DB]*Writer{}}

// A closed handle's Writer goes with it; a later Open makes a new handle.
func init() {
	database.OnClose(func(db *sql.DB) {
		writers.Lock()
		delete(writers.byDB, db)
		writers.Unlock()
	})
}

// For returns this process's Writer for db, making it on first use. Stores
// open before the engine sets its publish function, so For never needs one.
func For(db *sql.DB) *Writer {
	writers.Lock()
	defer writers.Unlock()
	w := writers.byDB[db]
	if w == nil {
		w = newWriter(db)
		writers.byDB[db] = w
	}
	return w
}

func newWriter(db *sql.DB) *Writer { return &Writer{db: db} }

// SetPublish sends each later committed change to publish, for example the
// engine's event hub. Without it, a Writer publishes nothing. For Write it
// runs under the database writer lock, and for Append under the Writer's
// append lock, so events follow commit order within each store and each log.
// Neither lock is reentrant: publish must not block, and must never write
// state. A Write and an Append can publish at the same time, so publish must
// also be safe for concurrent use.
func (w *Writer) SetPublish(publish func(hub.Event)) { w.publish.Store(&publish) }

type done struct {
	at   time.Time
	took time.Duration
	kind string
}

const (
	// slowWrite is the duration above which a write is logged.
	slowWrite = 100 * time.Millisecond
	// window is how long finished writes count for the percentiles.
	window = 5 * time.Minute
	// keepRecent bounds the finished writes kept for one window.
	keepRecent = 10000
	// stuckWrite marks the engine as degraded.
	stuckWrite = 5 * time.Second
)

// Write runs fn in one writer transaction. Nothing is published when ctx
// ends first or fn fails. fn and the helpers it calls may run any SQL on the
// conn they are given: the ratchet counts the transaction, not each statement.
// The change is published under the writer lock, so events follow commit
// order (item 14).
//
// When ctx carries a request ID (ForRequest), the call's first write also
// records it, in the same transaction. A write whose ID was already applied
// does nothing and returns ErrRepeated (requirement 9).
func (w *Writer) Write(ctx context.Context, c hub.Event, fn func(*sql.Conn) error) error {
	fn, req := w.recordFirst(ctx, fn)
	id, start := w.begin()
	err := database.ImmediateThen(ctx, w.db, fn, func() { w.notify(c) })
	w.end(id, start, c)
	if err == nil && req != nil {
		req.recorded.Store(true)
	}
	return err
}

// Append adds one line to a JSONL log. line must not contain a newline.
func (w *Writer) Append(ctx context.Context, c hub.Event, path string, line []byte) error {
	return w.AppendRecord(ctx, c, path, func(io.ReaderAt, int64) ([]byte, error) { return line, nil })
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Writer) begin() (uint64, time.Time) {
	start := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inFlight == nil {
		w.inFlight = map[uint64]time.Time{}
	}
	w.next++
	w.inFlight[w.next] = start
	return w.next, start
}

// end records a finished write. Its time includes the wait for the writer
// lock, so a long queue shows as slow writes too. database.transact logs the
// same write again, split into BEGIN wait, body and COMMIT, when its hold is
// slow: keep both lines, because only this one counts the in-process queue.
func (w *Writer) end(id uint64, start time.Time, c hub.Event) {
	at := w.now()
	took := at.Sub(start)
	w.mu.Lock()
	delete(w.inFlight, id)
	w.recent = append(w.recent, done{at, took, c.Topic})
	w.trim(at)
	w.mu.Unlock()
	if took > slowWrite {
		w.logf("state: slow write %s %s %s took %s (including the wait for the writer lock)", c.Topic, c.Room, c.Key, took.Round(time.Millisecond))
	}
}

// logf reports through Logf, or log.Printf without one.
func (w *Writer) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// trim drops finished writes older than the window, and the oldest beyond
// keepRecent. The caller holds w.mu.
func (w *Writer) trim(at time.Time) {
	cut := max(len(w.recent)-keepRecent, 0)
	for cut < len(w.recent) && at.Sub(w.recent[cut].at) > window {
		cut++
	}
	w.recent = slices.Delete(w.recent, 0, cut)
}

// notify publishes c. An event with no topic is not published: its store
// still publishes another way (posts, from the room watch, until item 16).
func (w *Writer) notify(c hub.Event) {
	if c.Topic == "" {
		return
	}
	if publish := w.publish.Load(); publish != nil && *publish != nil {
		(*publish)(c)
	}
}

// Health is the write path's part of the engine's Health call.
type Health struct {
	// Writes finished in the last 5 minutes, with their percentiles.
	Writes      int           `json:"writes"`
	P50         time.Duration `json:"p50Ns"`
	P99         time.Duration `json:"p99Ns"`
	Slowest     time.Duration `json:"slowestNs"`
	SlowestKind string        `json:"slowestKind,omitempty"`
	// InFlight counts this Writer's writes that were called and have not
	// ended. The queue for the writer lock is database.Waiting.
	InFlight int           `json:"inFlight"`
	Oldest   time.Duration `json:"oldestNs"`
	// Degraded is set when the oldest write is older than 5 s.
	Degraded bool `json:"degraded"`
}

// Health reports the last 5 minutes of writes and the writes in flight. A
// nil Writer (a read-only engine) reports nothing.
func (w *Writer) Health() Health {
	if w == nil {
		return Health{}
	}
	at := w.now()
	w.mu.Lock()
	w.trim(at)
	took := make([]time.Duration, len(w.recent))
	var h Health
	for i, d := range w.recent {
		took[i] = d.took
		if d.took > h.Slowest {
			h.Slowest, h.SlowestKind = d.took, d.kind
		}
	}
	h.InFlight = len(w.inFlight)
	for _, start := range w.inFlight {
		h.Oldest = max(h.Oldest, at.Sub(start))
	}
	w.mu.Unlock()
	h.Writes = len(took)
	if len(took) > 0 {
		slices.Sort(took)
		h.P50 = took[(len(took)-1)*50/100]
		h.P99 = took[(len(took)-1)*99/100]
	}
	h.Degraded = h.Oldest > stuckWrite
	return h
}
