package sessionhost

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// socketDir returns a short private directory for host sockets.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "dh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// realHost returns a client host whose hosts are real detached processes,
// and stops them when the test ends.
func realHost(t *testing.T) *socketHost {
	t.Helper()
	h := newSocketHost(socketDir(t))
	t.Cleanup(h.shutdownUsed)
	return h
}

// inProcessHost serves key from a server inside the test process, so tests
// can watch its internals. The socket listens where a client looks for it.
type inProcessHost struct {
	*socketHost
	sv   *server
	key  string
	done chan struct{}
}

func newInProcessHost(t *testing.T, key string) *inProcessHost {
	t.Helper()
	h := newSocketHost(socketDir(t))
	socket, _ := hostPaths(h.dir, "fake\x00"+key)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	sv := newServer(ln)
	done := make(chan struct{})
	go func() { sv.run(); close(done) }()
	t.Cleanup(func() { sv.shutdown(); <-done; h.shutdownUsed() })
	return &inProcessHost{socketHost: h, sv: sv, key: key, done: done}
}

// session returns the host-side session for the host's key.
func (p *inProcessHost) session() *localSession {
	p.sv.host.mu.Lock()
	defer p.sv.host.mu.Unlock()
	return p.sv.host.sessions["fake\x00"+p.key]
}

// TestSocketSessionRoundTripAndExtension proves frames, stderr and exit
// diagnostics cross the socket and the extension surface works remotely.
func TestSocketSessionRoundTripAndExtension(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	s, err := h.Session(context.Background(), childLaunch("echo", "fake:rt", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got := roundTrip(t, s, "hello"); got != "got:hello" {
		t.Fatalf("answer = %q", got)
	}
	if len(pids.all()) != 1 || len(refs.all()) != 1 {
		t.Fatalf("spawns %v binds %d, want one each", pids.all(), len(refs.all()))
	}
	ext := s.(LocalExtension)
	ext.KillGroup()
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	if _, err := s.Next(ctx); err == nil || !strings.Contains(err.Error(), "session process exited") {
		t.Fatalf("next after kill = %v, want an exit error", err)
	}
	s.Detach(DetachIdle)
}

// TestSocketSessionReportsChildStderr proves a failing child's stderr rides
// the exit error and the extension tail across the socket.
func TestSocketSessionReportsChildStderr(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	s, err := h.Session(context.Background(), childLaunch("fail", "fake:fail", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	if _, err := s.Next(ctx); err == nil || !strings.Contains(err.Error(), "fake child boom diagnostic") {
		t.Fatalf("next error = %v, want the child's stderr", err)
	}
	s.Detach(DetachIdle)
}

// TestSocketHostReusesChildAcrossProcesses proves the child outlives the
// attaching process: a second process attaching the same key gets the same
// guard reference (pid and birth) without spawning, and a held key refuses
// a second process with the same or a different fingerprint.
func TestSocketHostReusesChildAcrossProcesses(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "fake:cross", "f1", pids, refs)
	launch.Connection = []byte("private-endpoint")
	s1, err := h.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if string(s1.(ConnectionSession).Connection()) != "private-endpoint" {
		t.Fatal("lost private connection data")
	}
	clientRun := func(fingerprints string) []string {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "GO_SESSIONHOST_CLIENT_DIR="+h.dir,
			"GO_SESSIONHOST_CLIENT_KEY=fake:cross", "GO_SESSIONHOST_CLIENT_FP="+fingerprints)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("client process: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			t.Fatal("client process printed nothing")
		}
		return lines
	}

	refused := clientRun("f1,f2")
	if len(refused) != 2 {
		t.Fatalf("held key: client printed %d lines %q, want one refusal per fingerprint", len(refused), refused)
	}
	for i, line := range refused {
		if !strings.HasPrefix(line, "error=") || !strings.Contains(line, "fake:cross") {
			t.Fatalf("held key, try %d: client printed %q, want a refusal naming the key", i, line)
		}
	}
	if got := roundTrip(t, s1, "mine"); got != "got:mine" {
		t.Fatalf("held child answer = %q", got)
	}
	s1.Detach(DetachIdle)
	h.waitDetached(context.Background(), launch.id()) // the other process starts after the release

	pid, birth := refs.all()[0].Leader()
	want := "started=false leader=" + strconv.Itoa(pid) + ":" + birth + " answer=got:x"
	if got := clientRun("f1"); len(got) != 1 || got[0] != want {
		t.Fatalf("second process printed %q, want %q (same pid and birth, no spawn)", got, want)
	}
	if got := pids.all(); len(got) != 1 {
		t.Fatalf("spawns = %v, want the one original child", got)
	}
}

// TestSocketHostDifferentKeysStartIndependently proves each key gets its own
// host and child.
func TestSocketHostDifferentKeysStartIndependently(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	a, err := h.Session(context.Background(), childLaunch("echo", "fake:a", "f1", pids, refs))
	if err != nil {
		t.Fatalf("key a: %v", err)
	}
	b, err := h.Session(context.Background(), childLaunch("echo", "fake:b", "f1", pids, refs))
	if err != nil {
		t.Fatalf("key b: %v", err)
	}
	if got := roundTrip(t, a, "a"); got != "got:a" {
		t.Fatalf("a answer = %q", got)
	}
	if got := roundTrip(t, b, "b"); got != "got:b" {
		t.Fatalf("b answer = %q", got)
	}
	bound := refs.all()
	if len(bound) != 2 || bound[0] == bound[1] {
		t.Fatalf("keys shared a child: %v", bound)
	}
	if a.(*remoteSession).hostPid == b.(*remoteSession).hostPid {
		t.Fatal("keys shared a host process")
	}
	a.Detach(DetachIdle)
	b.Detach(DetachIdle)
}

// TestSocketSessionReplace proves a replacement over the socket starts a new
// child, binds its new reference, and drops the old child's frames.
func TestSocketSessionReplace(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	s, err := h.Session(context.Background(), childLaunch("echo", "fake:swap", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got := roundTrip(t, s, "old"); got != "got:old" {
		t.Fatalf("old answer = %q", got)
	}
	if err := s.Replace(context.Background(), childLaunch("echo", "fake:swap", "f2", pids, refs)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := roundTrip(t, s, "new"); got != "got:new" {
		t.Fatalf("new answer = %q", got)
	}
	s.Detach(DetachIdle)
	bound, spawned := refs.all(), pids.all()
	if len(bound) != 2 || bound[0] == bound[1] || len(spawned) != 2 {
		t.Fatalf("binds %v spawns %v, want a new child and reference", bound, spawned)
	}
}

// TestSocketHostRecoversFromDeadHost proves a killed host leaves a stale
// socket that the next attach replaces with a fresh host and child.
func TestSocketHostRecoversFromDeadHost(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "fake:stale", "f1", pids, refs)
	s1, err := h.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	r := s1.(*remoteSession)
	r.kill()
	waitClosed(t, r.gone, "host connection close")

	s2, err := h.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("session after dead host: %v", err)
	}
	if got := roundTrip(t, s2, "fresh"); got != "got:fresh" {
		t.Fatalf("fresh answer = %q", got)
	}
	s2.Detach(DetachIdle)
	bound := refs.all()
	if len(bound) != 2 || bound[0] == bound[1] || len(pids.all()) != 2 {
		t.Fatalf("binds %v spawns %v, want a fresh child", bound, pids.all())
	}
}

// TestSocketHostAbandonedDetachKillsGroup proves both abandonment paths end
// the child: an explicit DetachAbandoned and a client that vanished.
func TestSocketHostAbandonedDetachKillsGroup(t *testing.T) {
	for _, vanish := range []bool{false, true} {
		name := "detach"
		if vanish {
			name = "vanish"
		}
		t.Run(name, func(t *testing.T) {
			p := newInProcessHost(t, "fake:abandon-"+name)
			pids, refs := &pidRecorder{}, &refRecorder{}
			s, err := p.Session(context.Background(), childLaunch("echo", p.key, "f1", pids, refs))
			if err != nil {
				t.Fatalf("session: %v", err)
			}
			c := p.session().last
			if vanish {
				_ = s.(*remoteSession).conn.Close()
			} else {
				s.Detach(DetachAbandoned)
			}
			waitClosed(t, c.exited, "child exit after abandonment")
			waitClosed(t, p.done, "host exit with no child and no client")
		})
	}
}

// TestSocketHostIdleDetachKeepsChildUntilIdleClose proves a clean detach
// keeps the child, the idle trigger then closes it, and the empty host
// exits so the next attach starts a fresh host.
func TestSocketHostIdleDetachKeepsChildUntilIdleClose(t *testing.T) {
	p := newInProcessHost(t, "fake:idle")
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", p.key, "f1", pids, refs)
	s1, err := p.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if got := roundTrip(t, s1, "warm"); got != "got:warm" {
		t.Fatalf("warm answer = %q", got)
	}
	s1.Detach(DetachIdle)
	ls := p.session()
	if ls == nil {
		t.Fatal("clean detach dropped the session")
	}
	first := ls.last

	s2, err := p.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if got := roundTrip(t, s2, "again"); got != "got:again" {
		t.Fatalf("reattach answer = %q", got)
	}
	s2.Detach(DetachIdle)
	p.waitDetached(context.Background(), launch.id()) // idle close must not race the detach
	if got := pids.all(); len(got) != 1 {
		t.Fatalf("spawns = %v, want the child kept across a clean detach", got)
	}

	ls.idleClose()
	waitClosed(t, first.exited, "idle close of the child")
	waitClosed(t, p.done, "host exit after idle close")
}

// TestSocketHostBindsEachAttach proves BindLease sees the same reference on
// every attach of one child, and a bind failure leaves the child usable.
func TestSocketHostBindsEachAttach(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "fake:bind", "f1", pids, refs)
	s1, err := h.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	s1.Detach(DetachIdle)
	refs.fail = context.Canceled
	if _, err := h.Session(context.Background(), launch); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("attach with failing bind = %v, want the bind error", err)
	}
	refs.fail = nil
	s2, err := h.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("attach after failed bind: %v", err)
	}
	s2.Detach(DetachIdle)
	bound := refs.all()
	if len(bound) != 2 || bound[0] != bound[1] || len(pids.all()) != 1 {
		t.Fatalf("binds %v spawns %v, want one child bound twice", bound, pids.all())
	}
}

// TestSocketSessionCloseFencesFrames proves Close returns only after every
// frame the child wrote is readable, so nothing arrives after a caller's
// post-Close classification.
func TestSocketSessionCloseFencesFrames(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	fifo := filepath.Join(t.TempDir(), "written")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	launch := childLaunch("fence", "fake:fence", "f1", pids, refs)
	launch.Env = append(launch.Env, "GO_SESSIONHOST_FENCE="+fifo)
	s, err := h.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	// The open returns once the child has opened its end, after it wrote the frame.
	f, err := os.Open(fifo)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, f)
	f.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	got, err := s.Next(ctx)
	if err != nil || string(got) != "last" {
		t.Fatalf("next after close = %q, %v, want the frame written before close", got, err)
	}
	if _, err := s.Next(ctx); err == nil {
		t.Fatal("second next after close succeeded, want the exit error")
	}
	_ = s.(LocalExtension).StderrTail()
}

// A test that points TMPDIR at a long per-test folder must still get a
// socket path a Unix socket can bind.
func TestDefaultDirStaysShortUnderLongTempDir(t *testing.T) {
	long := t.TempDir() + "/" + strings.Repeat("x", 80)
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	dir := defaultDir()
	t.Cleanup(func() { os.RemoveAll(dir) })
	if socket, _ := hostPaths(dir, "key"); len(socket) > 104 {
		t.Fatalf("socket path is %d bytes: %s", len(socket), socket)
	}
}

func TestSocketCloseKeyStopsEveryAccountHome(t *testing.T) {
	h := realHost(t)
	pids, refs := &pidRecorder{}, &refRecorder{}
	start := func(key string) Session {
		t.Helper()
		s, err := h.Session(t.Context(), childLaunch("echo", key, "f1", pids, refs))
		if err != nil {
			t.Fatal(err)
		}
		if got := roundTrip(t, s, "ready"); got != "got:ready" {
			t.Fatal(got)
		}
		return s
	}
	a := start("session/closed\x00/account-a")
	b := start("session/closed\x00/account-b")
	other := start("session/other\x00/account-a")
	if err := h.closeKey(t.Context(), "session/closed"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []Session{a, b} {
		if err := s.Write([]byte("after close")); err == nil {
			t.Fatal("closed session retained a live child")
		}
		s.Detach(DetachIdle)
	}
	if got := roundTrip(t, other, "still alive"); got != "got:still alive" {
		t.Fatal("closed another session")
	}
	other.Detach(DetachIdle)
}
