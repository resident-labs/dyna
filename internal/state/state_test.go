package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
)

// clock is a fake clock that tests move by hand.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func testWriter(t *testing.T) (*Writer, *[]hub.Event) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec("CREATE TABLE things(name TEXT)"); err != nil {
		t.Fatal(err)
	}
	var published []hub.Event
	var mu sync.Mutex // writers publish concurrently
	// The database's Writer, so a close drains it: a cleanup that closes the
	// database waits for a publish that is still running (D-47).
	w := For(db)
	w.SetPublish(func(c hub.Event) {
		// The change is visible to readers when it is published.
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM things").Scan(&n); err != nil {
			t.Error(err)
		}
		c.Key = fmt.Sprintf("%s rows=%d", c.Key, n)
		mu.Lock()
		published = append(published, c)
		mu.Unlock()
	})
	return w, &published
}

func insertThing(conn *sql.Conn) error {
	_, err := conn.ExecContext(context.Background(), "INSERT INTO things(name) VALUES('a')")
	return err
}

func TestWriterPublishesAfterCommit(t *testing.T) {
	w, published := testWriter(t)
	failed := errors.New("failed")
	if err := w.Write(context.Background(), hub.Event{Topic: "things", Key: "bad"}, func(conn *sql.Conn) error {
		if err := insertThing(conn); err != nil {
			return err
		}
		return failed
	}); err != failed {
		t.Fatalf("failed write returned %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Write(ctx, hub.Event{Topic: "things", Key: "cancelled"}, insertThing); err != context.Canceled {
		t.Fatalf("cancelled write returned %v", err)
	}
	if err := w.Write(context.Background(), hub.Event{Topic: "things", Room: "room_1", Key: "good"}, insertThing); err != nil {
		t.Fatal(err)
	}
	want := []hub.Event{{Topic: "things", Room: "room_1", Key: "good rows=1"}}
	if !reflect.DeepEqual(*published, want) {
		t.Fatalf("published %v; want %v (once, after the commit, nothing for failed or cancelled writes)", *published, want)
	}
}

func TestWriterAppendsOneLine(t *testing.T) {
	w, published := testWriter(t)
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := w.Append(context.Background(), hub.Event{Topic: "posts"}, path, []byte("{}\n{}")); err == nil {
		t.Fatal("a line with a newline was appended")
	}
	// Spare capacity after the line must stay untouched.
	buf := make([]byte, 0, 32)
	line := append(buf, `{"a":1}`...)
	spare := line[:len(line)+1]
	spare[len(line)] = 'x'
	for range 2 {
		if err := w.Append(context.Background(), hub.Event{Topic: "posts", Room: "room_1"}, path, line); err != nil {
			t.Fatal(err)
		}
	}
	if spare[len(line)] != 'x' {
		t.Fatal("Append wrote into the caller's spare capacity")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"a\":1}\n{\"a\":1}\n" || len(*published) != 2 {
		t.Fatalf("log %q, %d published; want two lines and two changes", got, len(*published))
	}
}

func TestWriterLogsSlowWrite(t *testing.T) {
	w, _ := testWriter(t)
	c := &clock{now: time.Unix(0, 0)}
	w.Now = c.Now
	var logged []string
	w.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	if err := w.Write(context.Background(), hub.Event{Topic: "things", Room: "room_1"}, func(conn *sql.Conn) error {
		c.Add(150 * time.Millisecond)
		return insertThing(conn)
	}); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || logged[0] != "state: slow write things room_1  took 150ms (including the wait for the writer lock)" {
		t.Fatalf("logged %q; want one slow-write line", logged)
	}
}

func TestNilWriterHealth(t *testing.T) {
	var w *Writer
	if h := w.Health(); h != (Health{}) {
		t.Fatalf("a read-only engine's writer reported %+v", h)
	}
}

func TestWriterHealth(t *testing.T) {
	w, _ := testWriter(t)
	c := &clock{now: time.Unix(0, 0)}
	w.Now = c.Now
	w.Logf = func(string, ...any) {}
	for _, took := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 200 * time.Millisecond} {
		kind := fmt.Sprint("took-", took)
		if err := w.Write(context.Background(), hub.Event{Topic: kind}, func(conn *sql.Conn) error { c.Add(took); return insertThing(conn) }); err != nil {
			t.Fatal(err)
		}
	}
	h := w.Health()
	if h.Writes != 3 || h.P50 != 20*time.Millisecond || h.P99 != 20*time.Millisecond || h.Slowest != 200*time.Millisecond || h.SlowestKind != "took-200ms" || h.InFlight != 0 || h.Degraded {
		t.Fatalf("health after three writes: %+v", h)
	}

	// One write holds the writer lock; a second waits behind it.
	running, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- w.Write(context.Background(), hub.Event{Topic: "stuck"}, func(conn *sql.Conn) error {
			close(running)
			<-release
			return insertThing(conn)
		})
	}()
	<-running
	second := make(chan error, 1)
	go func() { second <- w.Write(context.Background(), hub.Event{Topic: "waiting"}, insertThing) }()
	waitFor(t, func() bool { return w.Health().InFlight == 2 && database.Waiting() == 1 })
	c.Add(6 * time.Second)
	h = w.Health()
	if h.Oldest != 6*time.Second || !h.Degraded {
		t.Fatalf("health with a stuck write: %+v; want oldest 6s, degraded", h)
	}
	close(release)
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	// Writes older than the window no longer count.
	c.Add(window + time.Second)
	if h := w.Health(); h.Writes != 0 || h.InFlight != 0 || h.Degraded {
		t.Fatalf("health after the window: %+v", h)
	}
}

// waitFor waits for a goroutine to reach a state that has no event of its
// own. It yields instead of sleeping, and the test deadline bounds it.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for !ok() {
		select {
		case <-t.Context().Done():
			t.Fatal("state never reached")
		default:
			runtime.Gosched()
		}
	}
}

// An event with no topic is not published: posts still publish from the
// room watch until item 16.
func TestEventWithoutTopicIsNotPublished(t *testing.T) {
	w, published := testWriter(t)
	path := filepath.Join(t.TempDir(), "log.jsonl")
	if err := w.Append(context.Background(), hub.Event{}, path, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if len(*published) != 0 {
		t.Fatalf("published %+v for an event with no topic", *published)
	}
}
