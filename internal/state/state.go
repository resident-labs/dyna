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

	events     sync.Mutex  // guards pending, publishing and idle
	pending    []hub.Event // committed changes waiting to publish, oldest first
	publishing bool        // a caller or the background goroutine publishes
	idle       *sync.Cond  // signals that publishing became false

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
// engine's event hub. Without it, a Writer publishes nothing. Write and
// Append queue the change under the lock that orders it (the database
// writer lock or the append lock) and publish it after that lock is
// released, so events follow commit order within each store and each log,
// and publish never runs under the writer (issue #247). publish runs on one
// goroutine at a time: the writing caller publishes its own change before
// it returns, unless an earlier change is still publishing. Then the change
// waits in the queue, the caller returns at once, and a background
// goroutine publishes it; WaitPublished waits for it. So a publish that
// waits for a lock never blocks a writer, and a caller never runs another
// caller's publish under its own locks. publish must still not take a lock
// that the writing caller can hold, and must not call WaitPublished.
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
// The change is queued under the writer lock, so events follow commit
// order (item 14), and published after the lock is released.
//
// When ctx carries a request ID (ForRequest), the call's first write also
// records it, in the same transaction. A write whose ID was already applied
// does nothing and returns ErrRepeated (requirement 9).
func (w *Writer) Write(ctx context.Context, c hub.Event, fn func(*sql.Conn) error) error {
	fn, req := w.recordFirst(ctx, fn)
	id, start := w.begin()
	var publish func()
	err := database.ImmediateThen(ctx, w.db, fn, func() { publish = w.queue(c) })
	w.end(id, start, c)
	if publish != nil {
		publish()
	}
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

// queue records c in commit order. The caller holds the lock that orders c
// and, when the result is not nil, calls it after releasing every lock it
// can: it publishes c. When an earlier change is still publishing, c waits
// in the queue for the background goroutine and queue returns nil. An event
// with no topic is not published: its store still publishes another way
// (posts, from the room watch, until item 16).
func (w *Writer) queue(c hub.Event) func() {
	if c.Topic == "" {
		return nil
	}
	if publish := w.publish.Load(); publish == nil || *publish == nil {
		return nil
	}
	w.events.Lock()
	defer w.events.Unlock()
	if w.publishing {
		w.pending = append(w.pending, c)
		return nil
	}
	w.publishing = true
	return func() {
		defer w.handOff()
		w.publishOne(c)
	}
}

// handOff ends the caller's publish. Changes queued meanwhile go to a
// background goroutine, so the caller never runs another caller's publish
// under its own locks.
func (w *Writer) handOff() {
	w.events.Lock()
	defer w.events.Unlock()
	if len(w.pending) > 0 {
		go w.publishPending()
		return
	}
	w.idleLocked()
}

// publishPending publishes queued changes in order until none are left. It
// holds no lock while publish runs.
func (w *Writer) publishPending() {
	w.events.Lock()
	defer w.events.Unlock()
	for len(w.pending) > 0 {
		c := w.pending[0]
		w.pending = w.pending[1:]
		w.events.Unlock()
		w.publishOne(c)
		w.events.Lock()
	}
	w.idleLocked()
}

// idleLocked marks the queue empty with no publish running. The caller
// holds w.events.
func (w *Writer) idleLocked() {
	w.pending = nil
	w.publishing = false
	if w.idle != nil {
		w.idle.Broadcast()
	}
}

// publishOne calls publish for c. A panic is logged, so later changes still
// publish and WaitPublished returns.
func (w *Writer) publishOne(c hub.Event) {
	defer func() {
		if r := recover(); r != nil {
			w.logf("state: publish %s %s %s panicked: %v", c.Topic, c.Room, c.Key, r)
		}
	}()
	if publish := w.publish.Load(); publish != nil && *publish != nil {
		(*publish)(c)
	}
}

// WaitPublished returns after every change committed before the call has
// been published. It must not be called from publish.
func (w *Writer) WaitPublished() {
	w.events.Lock()
	defer w.events.Unlock()
	if w.idle == nil {
		w.idle = sync.NewCond(&w.events)
	}
	for w.publishing {
		w.idle.Wait()
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
