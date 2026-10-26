package engine

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/database/databasetest"
	"dyna-agent/internal/processguard"
)

// The engine stops in under 1 s with work in flight (requirement 30): a
// worker inside its tick, a state write waiting for the writer lock, a
// process group with a child that holds its output open, and an open event
// stream. Each must end on the engine context, or the stop waits for its
// deadline and this test fails.
func TestEngineStopsUnderOneSecondWithWorkInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A worker inside its tick, as runWorkers runs them.
	signal := make(chan struct{}, 1)
	signal <- struct{}{}
	ticking := make(chan struct{})
	workers := make(chan struct{})
	go func() {
		defer close(workers)
		runWorker(ctx, signal, func(ctx context.Context) error { close(ticking); <-ctx.Done(); return ctx.Err() }, func(error) {})
	}()
	<-ticking

	// A write waiting behind another writer that holds the lock.
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	holding, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- database.Immediate(context.Background(), db, func(*sql.Conn) error { close(holding); <-release; return nil })
	}()
	<-holding
	defer func() { close(release); <-held }()
	// Waiting is process-wide, so count from its value before this waiter.
	before := database.Waiting()
	waiting := make(chan error, 1)
	go func() { waiting <- database.Immediate(ctx, db, func(*sql.Conn) error { return nil }) }()
	databasetest.WaitForWriter(t, before, waiting)

	// A process group whose grandchild keeps the output pipe open, as a git
	// fetch helper does.
	// Output goes to a Go writer, so Wait also waits for the copy, as
	// CombinedOutput does.
	started := &firstWrite{seen: make(chan struct{})}
	cmd := processguard.Command(ctx, "sh", "-c", "echo started; sleep 30 & sleep 30")
	cmd.Stdout = started
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	<-started.seen
	child := make(chan error, 1)
	go func() { child <- cmd.Wait() }()

	// An open event stream on a server whose requests use the engine context.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	streaming := make(chan struct{})
	srv := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: hello\n\n")
		w.(http.Flusher).Flush()
		close(streaming)
		<-r.Context().Done()
	})}
	go srv.Serve(ln)
	resp, err := http.Get("http://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if line, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil || !strings.HasPrefix(line, "data: hello") {
		t.Fatalf("stream did not open: %q, %v", line, err)
	}
	<-streaming

	start := time.Now()
	err = stopEngine(cancel, workers, []*http.Server{srv},
		func() {
			if err := <-waiting; err != context.Canceled {
				t.Errorf("the waiting write returned %v; want context.Canceled", err)
			}
		},
		func() { <-child })
	if took := time.Since(start); err != nil || took >= time.Second {
		t.Fatalf("stopEngine returned %v after %s; want nil in under 1s", err, took)
	}
}

// firstWrite closes seen on its first write.
type firstWrite struct {
	seen chan struct{}
	once sync.Once
}

func (w *firstWrite) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.seen) })
	return len(p), nil
}

// hangGuard only stops a broken run from blocking the suite; a passing run never
// reaches it.
const hangGuard = 10 * time.Second

// An open event stream must not hold the engine: a restart has to finish in
// seconds, not wait out the whole shutdown budget while every dyna command fails.
func TestStopEngineClosesOpenStreams(t *testing.T) {
	previous := streamShutdownDeadline
	streamShutdownDeadline = func() time.Duration { return 20 * time.Millisecond }
	t.Cleanup(func() { streamShutdownDeadline = previous })
	entered := make(chan struct{}, 1)
	blocked := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-blocked
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	// One client holds a request open, as a page's event stream does.
	stream, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := fmt.Fprintf(stream, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-entered
	t.Cleanup(func() { close(blocked) })

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- stopEngine(func() {}, nil, []*http.Server{server}) }()
	select {
	case <-done:
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("the engine took %s to stop with one open stream", elapsed)
		}
	case <-time.After(hangGuard):
		t.Fatal("the engine did not stop while a stream stayed open")
	}
}
