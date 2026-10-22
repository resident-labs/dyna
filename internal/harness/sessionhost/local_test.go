package sessionhost

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
)

// failDeadline bounds every event wait in these tests. Passing runs never
// wait on it; it only ends a hung run (r7).
const failDeadline = 30 * time.Second

// TestMain lets the test binary act as the fake harness child, and as a
// second client process for the cross-process tests. The detached host is
// this binary too: the package init serves it before TestMain runs.
//
// The helper exits go through syscall.Exit, not os.Exit: in a race-enabled
// binary os.Exit(0) runs the race detector's finalizer, which costs about a
// second per process on CI, and the fake children exit often. The fake echo
// child exits non-zero so the processguard guard that replays its status
// skips the same finalizer.
func TestMain(m *testing.M) {
	if dir := os.Getenv("GO_SESSIONHOST_CLIENT_DIR"); dir != "" {
		clientMain(dir)
		syscall.Exit(0)
	}
	switch os.Getenv("GO_SESSIONHOST_FAKE_CHILD") {
	case "echo":
		fakeEchoChild()
	case "fail":
		fakeFailChild()
	case "noisy":
		fakeNoisyChild()
	}
	os.Exit(m.Run())
}

// fakeEchoChild answers every stdin line with got:<line> on stdout and
// exits when stdin closes.
func fakeEchoChild() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fmt.Fprintf(os.Stdout, "got:%s\n", sc.Text())
	}
	syscall.Exit(7)
}

// fakeFailChild writes one diagnostic on stderr and exits 3, the way a
// launch, auth or rate-limit failure does.
func fakeFailChild() {
	fmt.Fprintln(os.Stderr, "fake child boom diagnostic")
	syscall.Exit(3)
}

// fakeNoisyChild writes far more stderr than the session keeps, ending with
// a marker, then exits 3.
func fakeNoisyChild() {
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 4*stderrTailLimit/len(line); i++ {
		fmt.Fprint(os.Stderr, line)
	}
	fmt.Fprintln(os.Stderr, "END-MARKER")
	syscall.Exit(3)
}

// clientMain is a second client process: it tries each fingerprint in order,
// prints one line per try, and stops after the first attach that succeeds,
// answering one round trip so the parent can compare child identity across
// processes. A refused attach prints its error and the next fingerprint
// tries too, so one subprocess can prove several refusals.
func clientMain(dir string) {
	started := false
	var ref processguard.Reference
	launch := Launch{
		Harness:   "fake",
		Key:       os.Getenv("GO_SESSIONHOST_CLIENT_KEY"),
		Argv:      []string{os.Args[0]},
		Env:       []string{"GO_SESSIONHOST_FAKE_CHILD=echo"},
		BindLease: func(r processguard.Reference) error { ref = r; return nil },
		OnStart:   func(int) { started = true },
	}
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	h := newSocketHost(dir)
	for _, fingerprint := range strings.Split(os.Getenv("GO_SESSIONHOST_CLIENT_FP"), ",") {
		launch.Fingerprint = fingerprint
		s, err := h.Session(ctx, launch)
		if err != nil {
			fmt.Printf("error=%v\n", err)
			continue
		}
		if err := s.Write([]byte("x")); err != nil {
			fmt.Printf("error=%v\n", err)
			return
		}
		got, err := s.Next(ctx)
		if err != nil {
			fmt.Printf("error=%v\n", err)
			return
		}
		pid, birth := ref.Leader()
		fmt.Printf("started=%t leader=%d:%s answer=%s\n", started, pid, birth, got)
		s.Detach(DetachIdle)
		h.waitDetached(ctx, launch.id()) // exit must not race the detach into an abandon
		return
	}
}

// pidRecorder collects the pids OnStart reported, in order.
type pidRecorder struct {
	mu   sync.Mutex
	pids []int
}

func (r *pidRecorder) onStart(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pids = append(r.pids, pid)
}

func (r *pidRecorder) all() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.pids...)
}

// refRecorder collects the references BindLease received, in order.
type refRecorder struct {
	mu   sync.Mutex
	refs []processguard.Reference
	fail error
}

func (r *refRecorder) bind(ref processguard.Reference) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.refs = append(r.refs, ref)
	return nil
}

func (r *refRecorder) all() []processguard.Reference {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]processguard.Reference(nil), r.refs...)
}

// childLaunch builds one launch of the fake child in the given mode.
func childLaunch(mode, key, fingerprint string, pids *pidRecorder, refs *refRecorder) Launch {
	return Launch{
		Harness:     "fake",
		Key:         key,
		Fingerprint: fingerprint,
		Argv:        []string{os.Args[0]},
		Env:         append(os.Environ(), "GO_SESSIONHOST_FAKE_CHILD="+mode),
		BindLease:   refs.bind,
		OnStart:     pids.onStart,
	}
}

// roundTrip writes one frame and returns the child's answer.
func roundTrip(t *testing.T, s Session, frame string) string {
	t.Helper()
	if err := s.Write([]byte(frame)); err != nil {
		t.Fatalf("write %q: %v", frame, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	got, err := s.Next(ctx)
	if err != nil {
		t.Fatalf("next after %q: %v", frame, err)
	}
	return string(got)
}

// waitClosed fails the test when ch is not closed by the failure deadline.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(failDeadline):
		t.Fatalf("%s never happened", what)
	}
}

// TestLocalSessionReusesChildAcrossAttaches proves the core reuse rule: a
// second turn on the same key and fingerprint attaches the same child, the
// spawn fires once, and every attach binds the same guard reference.
func TestLocalSessionReusesChildAcrossAttaches(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "reuse", "f1", pids, refs)

	s1, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if got := roundTrip(t, s1, "hello"); got != "got:hello" {
		t.Fatalf("first turn answer = %q", got)
	}
	s1.Detach(DetachIdle)

	s2, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("second session: %v", err)
	}
	if got := roundTrip(t, s2, "again"); got != "got:again" {
		t.Fatalf("second turn answer = %q", got)
	}
	s2.Detach(DetachIdle)

	if got := pids.all(); len(got) != 1 {
		t.Fatalf("spawns = %d, want 1 (pids %v)", len(got), got)
	}
	bound := refs.all()
	if len(bound) != 2 {
		t.Fatalf("binds = %d, want one per attach", len(bound))
	}
	if bound[0] != bound[1] {
		t.Fatal("attach bound a different reference for the same child")
	}
}

// TestLocalSessionRefusesSecondAttachWhileAttached proves one writer per
// session: a second attach while a turn holds the key is refused, with the
// same or a different fingerprint, and a refused changed fingerprint never
// replaces the held child.
func TestLocalSessionRefusesSecondAttachWhileAttached(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	s1, err := host.Session(context.Background(), childLaunch("echo", "held", "f1", pids, refs))
	if err != nil {
		t.Fatalf("first session: %v", err)
	}

	for _, fingerprint := range []string{"f1", "f2"} {
		_, err = host.Session(context.Background(), childLaunch("echo", "held", fingerprint, pids, refs))
		if err == nil || !strings.Contains(err.Error(), "held") {
			t.Fatalf("attach with %s error = %v, want refusal naming the key", fingerprint, err)
		}
	}
	if got := roundTrip(t, s1, "still"); got != "got:still" {
		t.Fatalf("held child answer = %q", got)
	}
	if got := pids.all(); len(got) != 1 {
		t.Fatalf("spawns = %d, want the refusal to leave the child alone", len(got))
	}

	s1.Detach(DetachIdle)
	s2, err := host.Session(context.Background(), childLaunch("echo", "held", "f1", pids, refs))
	if err != nil {
		t.Fatalf("attach after detach: %v", err)
	}
	s2.Detach(DetachIdle)
}

// TestLocalSessionDifferentKeysStartIndependently proves keys do not share
// a child.
func TestLocalSessionDifferentKeysStartIndependently(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	a, err := host.Session(context.Background(), childLaunch("echo", "fake:a", "f1", pids, refs))
	if err != nil {
		t.Fatalf("key a: %v", err)
	}
	b, err := host.Session(context.Background(), childLaunch("echo", "fake:b", "f1", pids, refs))
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
		t.Fatalf("keys shared a child reference: %v", bound)
	}
	a.Detach(DetachIdle)
	b.Detach(DetachIdle)
}

// TestLocalSessionReplaceOnFingerprintChange proves a changed launch
// identity replaces the child on the same session: a new pid, a new guard
// reference, and a working turn after.
func TestLocalSessionReplaceOnFingerprintChange(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}

	s1, err := host.Session(context.Background(), childLaunch("echo", "swap", "f1", pids, refs))
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	if got := roundTrip(t, s1, "old"); got != "got:old" {
		t.Fatalf("old child answer = %q", got)
	}
	s1.Detach(DetachIdle)

	s2, err := host.Session(context.Background(), childLaunch("echo", "swap", "f2", pids, refs))
	if err != nil {
		t.Fatalf("replaced session: %v", err)
	}
	if got := roundTrip(t, s2, "new"); got != "got:new" {
		t.Fatalf("new child answer = %q", got)
	}
	s2.Detach(DetachIdle)

	got := pids.all()
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("pids = %v, want two different spawns", got)
	}
	bound := refs.all()
	if len(bound) != 2 || bound[0] == bound[1] {
		t.Fatalf("references = %v, want a new one for the replacement", bound)
	}
}

// TestLocalSessionBindFailure proves a failed lease bind stops a spawn
// before the child runs, and leaves a live child in place on a later attach.
func TestLocalSessionBindFailure(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{fail: errors.New("lease refused")}
	if _, err := host.Session(context.Background(), childLaunch("echo", "bind", "f1", pids, refs)); err == nil || !strings.Contains(err.Error(), "lease refused") {
		t.Fatalf("spawn error = %v, want the bind error", err)
	}
	if got := pids.all(); len(got) != 0 {
		t.Fatalf("spawns = %v, want none after a failed bind", got)
	}

	refs.fail = nil
	s1, err := host.Session(context.Background(), childLaunch("echo", "bind", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	s1.Detach(DetachIdle)
	refs.fail = errors.New("lease refused")
	if _, err := host.Session(context.Background(), childLaunch("echo", "bind", "f1", pids, refs)); err == nil {
		t.Fatal("attach with a failed bind succeeded")
	}
	refs.fail = nil
	s2, err := host.Session(context.Background(), childLaunch("echo", "bind", "f1", pids, refs))
	if err != nil {
		t.Fatalf("attach after failed bind: %v", err)
	}
	if got := pids.all(); len(got) != 1 {
		t.Fatalf("spawns = %v, want the child kept after a failed attach bind", got)
	}
	s2.Detach(DetachIdle)
}

// TestLocalSessionNextReportsExitStderr proves a dead child surfaces
// through Next as an error carrying its stderr, that the dead entry is
// dropped so the next turn cold-starts, and that the extension reports the
// stderr window.
func TestLocalSessionNextReportsExitStderr(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}

	s, err := host.Session(context.Background(), childLaunch("fail", "dead", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	_, err = s.Next(ctx)
	if err == nil || !strings.Contains(err.Error(), "session process exited") || !strings.Contains(err.Error(), "fake child boom diagnostic") {
		t.Fatalf("next error = %v, want an exit error with the child's stderr", err)
	}
	if tail := s.(LocalExtension).StderrTail(); !strings.Contains(tail, "fake child boom diagnostic") {
		t.Fatalf("stderr tail = %q, want the diagnostic", tail)
	}
	s.Detach(DetachIdle)

	s2, err := host.Session(context.Background(), childLaunch("echo", "dead", "f1", pids, refs))
	if err != nil {
		t.Fatalf("cold start: %v", err)
	}
	if got := roundTrip(t, s2, "back"); got != "got:back" {
		t.Fatalf("cold-start answer = %q", got)
	}
	s2.Detach(DetachIdle)
	if got := pids.all(); len(got) != 2 {
		t.Fatalf("spawns = %d, want 2 (pids %v)", len(got), got)
	}
}

// TestLocalSessionBoundsStderr proves a noisy child keeps only the bounded
// tail of its stderr in memory and in its exit error.
func TestLocalSessionBoundsStderr(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	s, err := host.Session(context.Background(), childLaunch("noisy", "noisy", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	_, err = s.Next(ctx)
	if err == nil || !strings.HasSuffix(err.Error(), "END-MARKER") {
		t.Fatalf("next error does not end with the stderr marker: %v", err)
	}
	if len(err.Error()) > stderrTailLimit+200 {
		t.Fatalf("exit error is %d bytes, want at most the %d byte bound", len(err.Error()), stderrTailLimit)
	}
	s.Detach(DetachIdle)
}

// TestStderrWindowKeepsOnlyTurnBytes proves the per-turn window: bytes
// written before markTurn are not reported, the buffer stays bounded, and a
// turn sees the tail of what it wrote.
func TestStderrWindowKeepsOnlyTurnBytes(t *testing.T) {
	var w stderrWindow
	w.Write([]byte("old turn noise"))
	w.markTurn()
	if got := w.tail(false); got != "" {
		t.Fatalf("fresh window = %q, want empty", got)
	}
	w.Write([]byte("new"))
	if got := w.tail(false); got != "new" {
		t.Fatalf("window = %q, want only this turn's bytes", got)
	}
	big := strings.Repeat("y", 5*stderrTailLimit)
	w.Write([]byte(big))
	if got := w.tail(false); len(got) != stderrTailLimit {
		t.Fatalf("window = %d bytes, want the %d byte bound", len(got), stderrTailLimit)
	}
	if len(w.buf) > 2*stderrTailLimit {
		t.Fatalf("buffer holds %d bytes, want it bounded", len(w.buf))
	}
}

// TestLocalSessionIdleCloseColdStarts proves idle close stops a quiet
// session and drops it, so the next turn starts a fresh child, and that an
// attached session ignores the idle trigger.
func TestLocalSessionIdleCloseColdStarts(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "idle", "f1", pids, refs)

	s1, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}
	s1.(*localSession).idleClose()
	if got := roundTrip(t, s1, "warm"); got != "got:warm" {
		t.Fatalf("an attached session idle-closed: answer %q", got)
	}
	s1.Detach(DetachIdle)
	first := s1.(*localSession).last

	s1.(*localSession).idleClose()
	waitClosed(t, first.exited, "idle close")

	s2, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("session after idle: %v", err)
	}
	if got := roundTrip(t, s2, "cold"); got != "got:cold" {
		t.Fatalf("cold answer = %q", got)
	}
	s2.Detach(DetachIdle)
	if got := pids.all(); len(got) != 2 {
		t.Fatalf("spawns = %v, want a cold start after idle close", got)
	}
}

// TestLocalSessionAbandonedDetachKillsGroup proves an abandoned attachment
// kills the child's group at once and drops it, while a clean detach (see
// the reuse test) keeps it.
func TestLocalSessionAbandonedDetachKillsGroup(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "abandon", "f1", pids, refs)
	s1, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	c := s1.(*localSession).last
	s1.Detach(DetachAbandoned)
	waitClosed(t, c.exited, "child exit after abandoned detach")

	s2, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("session after abandon: %v", err)
	}
	s2.Detach(DetachIdle)
	if got := pids.all(); len(got) != 2 {
		t.Fatalf("spawns = %v, want a fresh child after an abandoned detach", got)
	}
}

// TestLocalSessionKilledChildRecovers proves a child killed between turns
// is reported through Next and replaced by the next attach.
func TestLocalSessionKilledChildRecovers(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	launch := childLaunch("echo", "killed", "f1", pids, refs)
	s1, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	s1.(LocalExtension).KillGroup()
	ctx, cancel := context.WithTimeout(context.Background(), failDeadline)
	defer cancel()
	if _, err := s1.Next(ctx); err == nil || !strings.Contains(err.Error(), "session process exited") {
		t.Fatalf("next error = %v, want an exit error", err)
	}
	s1.Detach(DetachIdle)

	s2, err := host.Session(context.Background(), launch)
	if err != nil {
		t.Fatalf("recovery session: %v", err)
	}
	if got := roundTrip(t, s2, "alive"); got != "got:alive" {
		t.Fatalf("recovered answer = %q", got)
	}
	s2.Detach(DetachIdle)
}

// TestLocalSessionHaltStopsChild proves Halt ends the child's group the way
// a canceled turn does, and the session then reports no child.
func TestLocalSessionHaltStopsChild(t *testing.T) {
	host := Local()
	pids, refs := &pidRecorder{}, &refRecorder{}
	s, err := host.Session(context.Background(), childLaunch("echo", "halt", "f1", pids, refs))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if got := roundTrip(t, s, "live"); got != "got:live" {
		t.Fatalf("live answer = %q", got)
	}
	s.(LocalExtension).Halt()
	if err := s.Write([]byte("gone")); err == nil {
		t.Fatal("write after halt succeeded, want no child")
	}
	s.Detach(DetachIdle)
}
