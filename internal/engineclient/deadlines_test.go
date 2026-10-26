package engineclient_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/websvc"
)

// A call the engine accepted but did not answer in time is not retried. The
// client fails with "engine did not answer", and the engine's request context
// carries the engine's own deadline.
func TestLateCallFailsOnceWithTheEngineDeadline(t *testing.T) {
	engineclient.SetDeadlines(t, time.Second, 100*time.Millisecond)
	var calls atomic.Int32
	left := make(chan time.Duration, 1)
	bound, err := websvc.Bind([]websvc.Binding{{Name: "Slow", Access: websvc.Read, Scope: websvc.ScopeRead,
		Method: func(ctx context.Context) error {
			calls.Add(1)
			if deadline, ok := ctx.Deadline(); ok {
				left <- time.Until(deadline)
			}
			<-ctx.Done()
			return ctx.Err()
		}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, t.TempDir(), bound)
	err = engineclient.New("").Call(t.Context(), "Slow", nil, nil)
	if err == nil || err.Error() != "engine did not answer Slow in 0.1 s" {
		t.Fatalf("late call = %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the engine ran the call %d times, want 1", n)
	}
	select {
	case d := <-left:
		if d <= 0 || d > 5*time.Second {
			t.Fatalf("engine request deadline in %v, want at most 5 s", d)
		}
	default:
		t.Fatal("the engine's request context had no deadline")
	}
}

// waitLine records what a client prints while it waits, and signals the
// first line.
type waitLine struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	first chan struct{}
}

func (w *waitLine) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() == 0 {
		close(w.first)
	}
	return w.buf.Write(p)
}

func (w *waitLine) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A call made while the engine is down waits, says so once, and is answered
// when the engine starts.
func TestCallWaitsForTheEngineToStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	waiting := &waitLine{first: make(chan struct{})}
	client := engineclient.New(path, engineclient.Notices(waiting))
	go func() {
		<-waiting.first
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Error(err)
			return
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`"up"`)) })}
		t.Cleanup(func() { _ = server.Close() })
		_ = server.Serve(listener)
	}()
	var reply string
	if err := client.Call(t.Context(), "Health", nil, &reply); err != nil || reply != "up" {
		t.Fatalf("call after start = %q, %v", reply, err)
	}
	if got := waiting.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "waiting for the engine") {
		t.Fatalf("waiting output = %q, want one waiting line", got)
	}
}

// A refused connection (a socket left by a stopped engine) is retried until
// the restart wait ends, then fails as engine down.
func TestRefusedSocketFailsAfterTheRestartWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	waiting := &waitLine{first: make(chan struct{})}
	client := engineclient.New(path, engineclient.RestartWait(300*time.Millisecond), engineclient.Notices(waiting))
	start := time.Now()
	err = client.Call(t.Context(), "Health", nil, nil)
	if !errors.Is(err, engineclient.ErrNotRunning) || !strings.Contains(err.Error(), "waited 0.3 s") {
		t.Fatalf("refused socket = %v", err)
	}
	if took := time.Since(start); took < 200*time.Millisecond {
		t.Fatalf("gave up after %v, before the restart wait", took)
	}
	if got := waiting.String(); strings.Count(got, "\n") != 1 {
		t.Fatalf("waiting output = %q, want one line", got)
	}
}

// An engine restart closes the connection before it answers. StartSubagent
// goes again once with the request ID it first sent, even when the first send
// used up the restart wait.
func TestCutStartSubagentRepeatsOnceWithTheSameRequestID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var ids []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ids = append(ids, r.Header.Get("X-Dyna-Request"))
		first := len(ids) == 1
		mu.Unlock()
		if first {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`"done"`))
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	var reply string
	if err := engineclient.New(path, engineclient.Notices(nil), engineclient.RestartWait(0)).Call(t.Context(), "StartSubagent", nil, &reply); err != nil || reply != "done" {
		t.Fatalf("call = %q, %v; want done after one retry", reply, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("request IDs = %q; want two sends with one non-empty ID", ids)
	}
}

// After the cut, a refused socket waits with a growing delay and says so once,
// instead of dialing in a tight loop. The clock is fake.
func TestCutThenRefusedSocketWaitsWithBackoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := listener.Accept()
		_ = listener.Close() // removes the socket, so later dials are refused
		if err == nil {
			_ = conn.Close()
		}
	}()
	at := time.Unix(0, 0)
	var waits []time.Duration
	var notices bytes.Buffer
	client := engineclient.New(path, engineclient.Notices(&notices), engineclient.RestartWait(time.Second), engineclient.FakeClock(&at, &waits))
	err = client.Call(t.Context(), "StartSubagent", nil, nil)
	if !errors.Is(err, engineclient.ErrNotRunning) {
		t.Fatalf("err = %v; want engine not running", err)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 300 * time.Millisecond}
	if !slices.Equal(waits, want) {
		t.Fatalf("waits = %v; want %v", waits, want)
	}
	if strings.Count(notices.String(), "waiting for the engine") != 1 {
		t.Fatalf("notices = %q; want one waiting line", notices.String())
	}
}

// lateEngine serves AddGoal on a socket. It creates a goal once per request ID,
// like the engine's apply-once wrapper, and holds the first send until the
// client gives up on it. The client deadline is 20 ms.
func lateEngine(t *testing.T, holdEvery bool) (path string, ids func() []string, created func() int) {
	t.Helper()
	engineclient.SetDeadlines(t, time.Second, 20*time.Millisecond)
	path = filepath.Join(t.TempDir(), "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	made := map[string]bool{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Dyna-Request")
		mu.Lock()
		seen = append(seen, id)
		first := len(seen) == 1
		made[id] = true
		mu.Unlock()
		if first || holdEvery {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`"g1"`))
	})}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	return path, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(seen) },
		func() int { mu.Lock(); defer mu.Unlock(); return len(made) }
}

// D-275: AddGoal was late but the engine created the goal. The client goes again
// once with the same request ID, so the engine creates one goal and answers.
func TestLateAddGoalRepeatsOnceWithTheSameRequestID(t *testing.T) {
	path, ids, created := lateEngine(t, false)
	var reply string
	if err := engineclient.New(path, engineclient.Notices(nil)).Call(t.Context(), "AddGoal", nil, &reply); err != nil || reply != "g1" {
		t.Fatalf("call = %q, %v; want g1 after one repeat", reply, err)
	}
	if got := ids(); len(got) != 2 || got[0] == "" || got[0] != got[1] || created() != 1 {
		t.Fatalf("request IDs %q, goals created %d; want two sends of one ID and one goal", got, created())
	}
}

// A creating call that is late twice stops after one repeat and says the engine
// may still apply it.
func TestLateAddGoalTwiceSaysTheEngineMayStillApplyIt(t *testing.T) {
	path, ids, _ := lateEngine(t, true)
	err := engineclient.New(path, engineclient.Notices(nil)).Call(t.Context(), "AddGoal", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "engine did not answer AddGoal") || !strings.Contains(err.Error(), "may still apply it") {
		t.Fatalf("err = %v", err)
	}
	if n := len(ids()); n != 2 {
		t.Fatalf("sends = %d, want 2", n)
	}
}
