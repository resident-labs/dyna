package engineclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
)

// fakeEngine records each call's method and request ID. drop lists the
// request IDs whose first attempt it applies and then answers with a closed
// connection, as an engine that stops before it replies. refuse lists the
// methods it answers with an error.
type fakeEngine struct {
	mu      sync.Mutex
	calls   []string
	dropped map[string]bool
	drop    map[string]bool
	refuse  map[string]bool
	missing map[string]bool            // methods it answers with 404, as an earlier engine
	busy    map[string]bool            // request IDs answered 500 once
	held    chan struct{}              // when set, the first call waits for it
	arrived chan struct{}              // closed when the held call arrives
	results map[string]json.RawMessage // successful method results
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	id := r.Header.Get(callspec.RequestHeader)
	f.mu.Lock()
	f.calls = append(f.calls, method+" "+id)
	drop := f.drop[id] && !f.dropped[id]
	if drop {
		f.dropped[id] = true
	}
	busy := f.busy[id]
	delete(f.busy, id)
	held := f.held
	f.held = nil
	f.mu.Unlock()
	if held != nil {
		close(f.arrived)
		<-held
	}
	if busy {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "database is busy"})
		return
	}
	if drop {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return
	}
	if f.missing[method] {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "the engine has no method " + method})
		return
	}
	if f.refuse[method] {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused for the test"})
		return
	}
	if result, ok := f.results[method]; ok {
		_, _ = w.Write(result)
	} else {
		_, _ = w.Write([]byte("null"))
	}
}

func (f *fakeEngine) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func serveFake(t *testing.T, f *fakeEngine) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return New(path)
}

// closeWithin closes o, failing the test if the queue does not drain within
// a bound far above the test's real time.
func closeWithin(t *testing.T, o *Outbox) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	o.Close(ctx)
	if ctx.Err() != nil {
		t.Fatal("outbox did not drain")
	}
}

// Writes reach the engine in order. A write whose reply is lost is sent
// again with the same request ID, so the engine can apply it once, and the
// writes behind it wait.
func TestOutboxSendsInOrderAndResendsWithTheSameID(t *testing.T) {
	f := &fakeEngine{drop: map[string]bool{"run/1": true}, dropped: map[string]bool{}}
	o := NewOutbox(serveFake(t, f), "run")
	o.Send("A")
	o.Send("B")
	o.Send("C")
	closeWithin(t, o)
	want := []string{"A run/1", "A run/1", "B run/2", "C run/3"}
	if got := f.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("engine saw %v; want %v", got, want)
	}
}

// A write the engine refuses is not sent again: it is logged, and the
// writes behind it go on. Call returns the refusal to its caller.
func TestOutboxLogsARefusedWriteAndGoesOn(t *testing.T) {
	f := &fakeEngine{refuse: map[string]bool{"Bad": true}}
	o := NewOutbox(serveFake(t, f), "run")
	var logged []string
	o.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	o.Send("Bad")
	err := o.Call(t.Context(), "Good")
	if err != nil {
		t.Fatalf("Call after a refused write = %v", err)
	}
	if err := o.Call(t.Context(), "Bad"); err == nil || !strings.Contains(err.Error(), "refused for the test") {
		t.Fatalf("Call of a refused write = %v; want the refusal", err)
	}
	closeWithin(t, o)
	if got := f.seen(); fmt.Sprint(got) != fmt.Sprint([]string{"Bad run/1", "Good run/2", "Bad run/3"}) {
		t.Fatalf("engine saw %v", got)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "Bad run/1") {
		t.Fatalf("logged %v; want each refused write", logged)
	}
}

// Close stops at its deadline when the engine stays down, and logs each
// write it loses, with its method and request ID.
func TestOutboxCloseLogsEachLostWrite(t *testing.T) {
	o := NewOutbox(noWait(filepath.Join(t.TempDir(), "engine.sock")), "run")
	var mu sync.Mutex
	var logged []string
	o.Logf = func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	o.Send("A")
	o.Send("B")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	o.Close(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 2 || !strings.Contains(logged[0], "A run/1") || !strings.Contains(logged[1], "B run/2") {
		t.Fatalf("logged %v; want both lost writes with their IDs", logged)
	}
}

// A 5xx reply (here a busy database) is not final: the write is sent again
// with the same request ID, and is not logged as refused.
func TestOutboxResendsAfterAServerError(t *testing.T) {
	f := &fakeEngine{busy: map[string]bool{"run/1": true}}
	o := NewOutbox(serveFake(t, f), "run")
	var logged []string
	o.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	o.Send("A")
	o.Send("B")
	closeWithin(t, o)
	want := []string{"A run/1", "A run/1", "B run/2"}
	if got := f.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("engine saw %v; want %v", got, want)
	}
	if len(logged) != 0 {
		t.Fatalf("logged %v; want nothing for a write that landed", logged)
	}
}

// When a Call's context ends while its write is still queued, the write is
// taken off the queue and never reaches the engine.
func TestOutboxCallGivesUpAWriteStillQueued(t *testing.T) {
	release := make(chan struct{})
	f := &fakeEngine{held: release, arrived: make(chan struct{})}
	o := NewOutbox(serveFake(t, f), "run")
	o.Send("A")
	<-f.arrived // A is being sent; the next write waits in the queue.
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- o.Call(ctx, "Pin") }()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("Call returned no error after its context ended")
	}
	close(release)
	closeWithin(t, o)
	if got := f.seen(); fmt.Sprint(got) != fmt.Sprint([]string{"A run/1"}) {
		t.Fatalf("engine saw %v; want only A, never the given-up Pin", got)
	}
}

// Close past its deadline hands the writes still queued to Spool, in order
// and with the request IDs they were sent under, and logs none as lost.
func TestOutboxCloseSpoolsWhatItCouldNotSend(t *testing.T) {
	o := NewOutbox(noWait(filepath.Join(t.TempDir(), "engine.sock")), "run")
	var mu sync.Mutex
	var logged []string
	o.Logf = func(format string, args ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	var spooled []Queued
	o.Spool = func(q []Queued) error { spooled = q; return nil }
	o.Send("A", 1)
	o.Send("B", 2)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	o.Close(ctx)
	if len(spooled) != 2 || spooled[0].ID != "run/1" || spooled[0].Method != "A" || spooled[1].ID != "run/2" || fmt.Sprint(spooled[1].Args) != "[2]" {
		t.Fatalf("spooled %+v; want A run/1 then B run/2 with their arguments", spooled)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 0 {
		t.Fatalf("logged %v; want no write logged as lost", logged)
	}
}

// A queue claim must return its accepted messages through the same ordered
// outbox, including when the engine commits it and loses the first reply.
func TestOutboxReturnsTheResultAfterAResend(t *testing.T) {
	f := &fakeEngine{
		drop: map[string]bool{"run/2": true}, dropped: map[string]bool{},
		results: map[string]json.RawMessage{"Claim": json.RawMessage(`{"messages":["msg_1"],"cursor":17}`)},
	}
	o := NewOutbox(serveFake(t, f), "run")
	defer closeWithin(t, o)
	o.Send("Before")
	var result struct {
		Messages []string `json:"messages"`
		Cursor   int64    `json:"cursor"`
	}
	if err := o.CallResult(t.Context(), &result, "Claim"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(result.Messages) != "[msg_1]" || result.Cursor != 17 {
		t.Fatalf("claim result = %+v; want msg_1 at cursor 17", result)
	}
	if err := o.Call(t.Context(), "After"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Before run/1", "Claim run/2", "Claim run/2", "After run/3"}
	if got := f.seen(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("engine saw %v; want %v", got, want)
	}
}

func TestCancelledQueuedCallNeverChangesItsResult(t *testing.T) {
	release := make(chan struct{})
	f := &fakeEngine{held: release, arrived: make(chan struct{}), results: map[string]json.RawMessage{"Claim": json.RawMessage(`17`)}}
	o := NewOutbox(serveFake(t, f), "run")
	defer func() {
		if release != nil {
			close(release)
		}
		closeWithin(t, o)
	}()
	o.Send("Before")
	<-f.arrived
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := 29
	if err := o.CallResult(ctx, &result, "Claim"); err == nil {
		t.Fatal("a cancelled queued claim returned no error")
	}
	close(release)
	release = nil
	closeWithin(t, o)
	if result != 29 {
		t.Fatalf("result = %d; a cancelled claim changed it", result)
	}
	if got := f.seen(); fmt.Sprint(got) != "[Before run/1]" {
		t.Fatalf("engine saw %v; want only the preceding write", got)
	}
}
