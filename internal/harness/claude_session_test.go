package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

const claudeFakePID = 4242

// claudeFakeHost stands in for the shared host. It keeps one fake Claude
// session per fingerprint, replaces it when the fingerprint changes, and
// refuses a second attach while one is attached.
type claudeFakeHost struct {
	mu              sync.Mutex
	launches        int
	pids            []int
	fingerprint     string
	session         *claudeFakeSession
	ignoreInterrupt bool
}

func (h *claudeFakeHost) Session(_ context.Context, launch sessionhost.Launch) (sessionhost.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.session == nil || h.fingerprint != launch.Fingerprint {
		if h.session != nil {
			_ = h.session.Close()
		}
		h.launches++
		h.fingerprint = launch.Fingerprint
		h.session = &claudeFakeSession{out: make(chan []byte, 64), ignoreInterrupt: h.ignoreInterrupt}
		if launch.OnStart != nil {
			launch.OnStart(claudeFakePID)
		}
		h.pids = append(h.pids, claudeFakePID)
	}
	if h.session.isAttached() {
		return nil, errors.New("fake session is already attached")
	}
	h.session.attach()
	return h.session, nil
}

// claudeFakeSession answers like the Claude CLI. A user message gets a replay
// and a result, unless its text contains "hold turn". Each set_model gets a
// success response. An interrupt gets a success response and an error result,
// unless ignoreInterrupt is set.
type claudeFakeSession struct {
	out             chan []byte
	ignoreInterrupt bool
	mu              sync.Mutex
	attached        bool
	sawInit         bool
	writes          [][]byte
	detaches        []sessionhost.DetachReason
	closed          int
}

func (s *claudeFakeSession) Write(frame []byte) error {
	s.mu.Lock()
	s.writes = append(s.writes, append([]byte(nil), frame...))
	first := !s.sawInit
	s.sawInit = true
	s.mu.Unlock()
	if first {
		s.emit(map[string]any{"type": "system", "subtype": "init", "session_id": "fake-session"})
	}
	var env struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal(frame, &env); err != nil {
		return err
	}
	switch {
	case env.Type == "user":
		var user struct {
			Message json.RawMessage `json:"message"`
		}
		_ = json.Unmarshal(frame, &user)
		s.emit(map[string]any{"type": "user", "isReplay": true, "message": user.Message})
		if !strings.Contains(string(frame), "hold turn") {
			s.emit(map[string]any{"type": "result", "subtype": "success", "session_id": "fake-session", "result": "done", "is_error": false})
		}
	case env.Type == "control_request" && env.Request.Subtype == "set_model":
		s.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": env.RequestID, "response": map[string]any{}}})
	case env.Type == "control_request" && env.Request.Subtype == "interrupt":
		if s.ignoreInterrupt {
			return nil
		}
		s.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": env.RequestID, "response": map[string]any{}}})
		s.emit(map[string]any{"type": "result", "subtype": "error_during_execution", "session_id": "fake-session", "result": "interrupted", "is_error": true})
	}
	return nil
}

func (s *claudeFakeSession) emit(record map[string]any) {
	b, _ := json.Marshal(record)
	s.out <- b
}

func (s *claudeFakeSession) Next(ctx context.Context) ([]byte, error) {
	select {
	case line := <-s.out:
		return line, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *claudeFakeSession) Replace(context.Context, sessionhost.Launch) error {
	return errors.New("replace is not used by the Claude turn")
}

func (s *claudeFakeSession) isAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attached
}

func (s *claudeFakeSession) attach() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = true
}

func (s *claudeFakeSession) Detach(reason sessionhost.DetachReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = false
	s.detaches = append(s.detaches, reason)
}

func (s *claudeFakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}

func (s *claudeFakeSession) snapshot() (writes [][]byte, detaches []sessionhost.DetachReason, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.writes...), append([]sessionhost.DetachReason(nil), s.detaches...), s.closed
}

func claudeHostedTestSetup(t *testing.T) *claudeFakeHost {
	t.Helper()
	host := &claudeFakeHost{}
	prev := nativeSessionHost
	nativeSessionHost = host
	t.Cleanup(func() { nativeSessionHost = prev })
	return host
}

func claudeHostedTurn(ctx context.Context, t *testing.T, dir, prompt string, steering *steeringSupervisor, liveLine func(string)) <-chan attempt {
	t.Helper()
	spec := commandSpec{prompt: prompt, streamInput: true, argv: []string{"/bin/sh", "-c", "exit 0"}}
	p := profile.Profile{Harness: profile.HarnessCustom, Model: "haiku"}
	execution := ExecutionOptions{SkipComputer: true, SessionKey: "claude-hosted-test"}
	done := make(chan attempt, 1)
	// The engine's tracker learns the id dyna itself passed (--session-id); a
	// fixed parser mirrors that. Without it the steering source never
	// activates, because activateIfReady needs the session id.
	tracker := newSessionTracker(func(string) string { return "claude-hosted-test" })
	go func() {
		done <- runOnce(ctx, p, dir, spec, tracker, nil, steering, true, "", "",
			&workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON}, liveLine,
			newDeliveryGate(JournalOptions{}), &runState{}, execution)
	}()
	return done
}

func awaitClaudeTurn(t *testing.T, done <-chan attempt, what string) attempt {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not finish", what)
	}
	return attempt{}
}

func TestClaudeHostedTurnsReuseOneProcess(t *testing.T) {
	host := claudeHostedTestSetup(t)
	ctx := t.Context()
	dir := t.TempDir() // one working directory: the fingerprint must not drift between turns
	for _, prompt := range []string{"first turn", "second turn"} {
		got := awaitClaudeTurn(t, claudeHostedTurn(ctx, t, dir, prompt, nil, nil), prompt)
		if got.runErr != nil {
			t.Fatalf("%s failed: %v\n%s", prompt, got.runErr, got.stdout)
		}
		if !strings.Contains(got.stdout, `"result":"done"`) {
			t.Fatalf("%s did not end on its result:\n%s", prompt, got.stdout)
		}
	}
	if host.launches != 1 || len(host.pids) != 1 {
		t.Fatalf("launches = %d pids = %v; want one shared process", host.launches, host.pids)
	}
	writes, detaches, closed := host.session.snapshot()
	if len(detaches) != 2 || detaches[0] != sessionhost.DetachIdle || detaches[1] != sessionhost.DetachIdle || closed != 0 {
		t.Fatalf("detaches = %v closed = %d; want two idle detaches and no close", detaches, closed)
	}
	for _, frame := range writes {
		if bytes.HasSuffix(frame, []byte("\n")) {
			t.Fatalf("frame kept a trailing LF, which the host adds: %q", frame)
		}
	}
	if n := countClaudeControl(writes, "set_model"); n != 2 {
		t.Fatalf("set_model writes = %d; want one per turn", n)
	}
}

func TestClaudeHostedInterruptKeepsIdleProcess(t *testing.T) {
	host := claudeHostedTestSetup(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	first := awaitClaudeTurn(t, claudeHostedTurn(ctx, t, dir, "first turn", nil, nil), "first turn")
	if first.runErr != nil {
		t.Fatalf("first turn failed: %v", first.runErr)
	}
	replayed := make(chan struct{}, 1)
	done := claudeHostedTurn(ctx, t, dir, "hold turn", nil, func(line string) {
		if strings.Contains(line, `"isReplay":true`) {
			select {
			case replayed <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-replayed:
	case got := <-done:
		t.Fatalf("held turn ended before the interrupt: %+v", got.runErr)
	case <-time.After(30 * time.Second):
		t.Fatal("held turn was not taken in")
	}
	cancel()
	got := awaitClaudeTurn(t, done, "interrupted turn")
	if !got.contextDone {
		t.Fatalf("interrupt did not finish the attempt: %v", got.runErr)
	}
	writes, detaches, closed := host.session.snapshot()
	if !claudeWroteControl(writes, "interrupt") {
		t.Fatal("interrupt control_request was not written to the same stdin")
	}
	if host.launches != 1 || closed != 0 {
		t.Fatalf("launches = %d closed = %d; want the idle process kept", host.launches, closed)
	}
	if len(detaches) != 2 || detaches[1] != sessionhost.DetachIdle {
		t.Fatalf("detaches = %v; want the interrupted process detached as idle", detaches)
	}
}

func TestClaudeHostedUnacknowledgedInterruptKillsChild(t *testing.T) {
	host := claudeHostedTestSetup(t)
	host.ignoreInterrupt = true
	prev := claudeInterruptGrace
	claudeInterruptGrace = 50 * time.Millisecond
	t.Cleanup(func() { claudeInterruptGrace = prev })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	replayed := make(chan struct{}, 1)
	done := claudeHostedTurn(ctx, t, dir, "hold turn", nil, func(line string) {
		if strings.Contains(line, `"isReplay":true`) {
			select {
			case replayed <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-replayed:
	case <-time.After(30 * time.Second):
		t.Fatal("held turn was not taken in")
	}
	cancel()
	got := awaitClaudeTurn(t, done, "unacknowledged interrupt")
	if !got.contextDone {
		t.Fatalf("interrupt did not finish the attempt: %v", got.runErr)
	}
	_, detaches, _ := host.session.snapshot()
	if len(detaches) != 1 || detaches[0] != sessionhost.DetachAbandoned {
		t.Fatalf("detaches = %v; want the unacknowledged child abandoned so the host kills it", detaches)
	}
}

func TestClaudeHostedSteeringUsesSameProcess(t *testing.T) {
	host := claudeHostedTestSetup(t)
	src := &testSource{}
	src.submit("steer now")
	var delivered []string
	steering := newSteeringSupervisor(SteeringOptions{
		Source:    src,
		pollEvery: 10 * time.Millisecond,
		OnMessage: func(m runstore.SteeringMessage, _ *int64) { delivered = append(delivered, m.Message) },
	}, true)
	got := awaitClaudeTurn(t, claudeHostedTurn(t.Context(), t, t.TempDir(), "first turn", steering, nil), "steered turn")
	if got.runErr != nil {
		t.Fatalf("steered turn failed: %v\n%s", got.runErr, got.stdout)
	}
	if !got.streamSteered {
		t.Fatal("native dispatch erased the successful steering receipt")
	}
	if len(delivered) != 1 || delivered[0] != "steer now" {
		t.Fatalf("delivered = %q; want the steering message once", delivered)
	}
	writes, detaches, _ := host.session.snapshot()
	if host.launches != 1 {
		t.Fatalf("launches = %d; want steering on the same process", host.launches)
	}
	if !bytes.Contains(bytes.Join(writes, nil), []byte("steer now")) {
		t.Fatal("steering text was not written to the shared stdin")
	}
	if len(detaches) != 1 || detaches[0] != sessionhost.DetachIdle {
		t.Fatalf("detaches = %v; want one idle detach after steering", detaches)
	}
}

func TestClaudeLaunchFingerprintIgnoresOnlySelector(t *testing.T) {
	env := []string{"HOME=/home/worker"}
	fresh := claudeLaunchFingerprint([]string{"claude", "-p", "--session-id", "s1", "--model", "haiku"}, env, "/w")
	resumed := claudeLaunchFingerprint([]string{"claude", "-p", "--resume", "s1", "--model", "haiku"}, env, "/w")
	if fresh != resumed {
		t.Fatal("--session-id and --resume of one session must share a fingerprint")
	}
	if other := claudeLaunchFingerprint([]string{"claude", "-p", "--resume", "s1", "--model", "opus"}, env, "/w"); other == resumed {
		t.Fatal("a changed model must change the fingerprint")
	}
	if moved := claudeLaunchFingerprint([]string{"claude", "-p", "--resume", "s1", "--model", "haiku"}, env, "/elsewhere"); moved == resumed {
		t.Fatal("a changed directory must change the fingerprint")
	}
}

func countClaudeControl(writes [][]byte, subtype string) int {
	n := 0
	for _, frame := range writes {
		var env struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if json.Unmarshal(frame, &env) == nil && env.Type == "control_request" && env.Request.Subtype == subtype {
			n++
		}
	}
	return n
}

func claudeWroteControl(writes [][]byte, subtype string) bool {
	return countClaudeControl(writes, subtype) > 0
}

func TestClaudeHostedHardCancelStopsChildImmediately(t *testing.T) {
	host := claudeHostedTestSetup(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dir := t.TempDir()
	p := profile.Profile{Harness: profile.HarnessCustom, Model: "haiku"}
	spec := commandSpec{prompt: "hold turn", streamInput: true, argv: []string{"/bin/sh", "-c", "exit 0"}}
	done := make(chan attempt, 1)
	go func() {
		done <- runOnce(ctx, p, dir, spec, newSessionTracker(nil), nil, nil, true, "", "",
			&workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON},
			func(line string) {
				if strings.Contains(line, "isReplay") {
					cancel()
				}
			},
			newDeliveryGate(JournalOptions{}), &runState{},
			ExecutionOptions{SkipComputer: true, SessionKey: "hard-cancel", KillOnCancel: true})
	}()
	got := awaitClaudeTurn(t, done, "hard deadline")
	writes, detaches, closed := host.session.snapshot()
	if !got.contextDone || closed != 1 || claudeWroteControl(writes, "interrupt") {
		t.Fatalf("hard deadline waited for native interrupt: canceled=%v closed=%d", got.contextDone, closed)
	}
	if len(detaches) != 1 || detaches[0] != sessionhost.DetachAbandoned {
		t.Fatalf("hard deadline detaches = %v", detaches)
	}
}
