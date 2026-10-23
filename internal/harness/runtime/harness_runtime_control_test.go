package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type controlFixture struct {
	mu              sync.Mutex
	running         int
	stops           int
	pending         bool
	start           chan struct{}
	activate        chan struct{}
	activated       chan struct{}
	finish          chan struct{}
	personalConsent bool
}

func controlApp(t *testing.T) (*Runtime, *controlFixture) {
	t.Helper()
	a := testPrivateRuntime(t)
	f := &controlFixture{running: 2, start: make(chan struct{}, 10), activate: make(chan struct{}), activated: make(chan struct{}), finish: make(chan struct{})}
	a.privateSwitch.hooks = &privateRuntimeControlHooks{
		prepare: func(id string, consent bool) (string, string, error) {
			if f.personalConsent && !consent {
				return "", "", errors.New("personal login consent required")
			}
			return "codex", "offline", nil
		},
		status: func(h string) (bool, int) { f.mu.Lock(); defer f.mu.Unlock(); return f.pending, f.running },
		stop:   func(ctx context.Context, h string) error { f.mu.Lock(); defer f.mu.Unlock(); f.stops++; return nil },
		switchAccount: func(ctx context.Context, id, exe string, onActivate func()) error {
			f.mu.Lock()
			f.pending = true
			f.mu.Unlock()
			defer func() { f.mu.Lock(); f.pending = false; f.mu.Unlock() }()
			f.start <- struct{}{}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-f.activate:
			}
			onActivate()
			// onActivate records the activating phase, so this reports a
			// switch that activation can no longer cancel or stop.
			close(f.activated)
			<-f.finish
			return nil
		},
		finish: func(string) (PrivateRuntimeSnapshot, error) { return PrivateRuntimeSnapshot{}, nil },
	}
	return a, f
}
func beginControlSwitch(t *testing.T, a *Runtime, f *controlFixture) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := a.SwitchPrivateRuntimeAccount("original", true); done <- err }()
	select {
	case <-f.start:
	case <-time.After(time.Second):
		t.Fatal("switch did not start")
	}
	return done
}
func awaitControlResult(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("switch did not finish")
		return nil
	}
}
func controlState(t *testing.T, a *Runtime) PrivateRuntimeSwitchControl {
	t.Helper()
	s, err := a.PrivateRuntimeSwitchState(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRuntimeControlCancelAndStaleRequest(t *testing.T) {
	a, f := controlApp(t)
	done := beginControlSwitch(t, a, f)
	first := controlState(t, a)
	if first.RequestID == "" || first.TargetAccountID != "original" || !first.CanCancel || !first.CanStop || first.RunningTurns != 2 {
		t.Fatalf("bad state: %+v", first)
	}
	if _, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", first.RequestID, false); err == nil {
		t.Fatal("stop without consent")
	}
	if f.stops != 0 {
		t.Fatal("stop called without consent")
	}
	state, err := a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", first.RequestID)
	if err != nil || state.Phase != "canceled" || state.RunningTurns != 2 {
		t.Fatalf("cancel: %+v %v", state, err)
	}
	if !errors.Is(awaitControlResult(t, done), context.Canceled) {
		t.Fatal("switch was not canceled")
	}
	done = beginControlSwitch(t, a, f)
	second := controlState(t, a)
	if second.RequestID == first.RequestID {
		t.Fatal("request ID reused")
	}
	if _, err := a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", first.RequestID); err == nil {
		t.Fatal("stale cancel accepted")
	}
	if _, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", first.RequestID, true); err == nil {
		t.Fatal("stale stop accepted")
	}
	if controlState(t, a).RequestID != second.RequestID || f.stops != 0 {
		t.Fatal("stale operation changed replacement")
	}
	a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", second.RequestID)
	awaitControlResult(t, done)
}
func TestRuntimeControlWorkflowBlockers(t *testing.T) {
	a, f := controlApp(t)
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	state, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID, true)
	if err == nil || state.RunningTurns != 2 || state.TargetAccountID != "original" || f.stops != 1 {
		t.Fatalf("remaining workflow blockers: %+v %v", state, err)
	}
	a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID)
	awaitControlResult(t, done)
}
func TestRuntimeControlPersonalConsent(t *testing.T) {
	a, f := controlApp(t)
	f.personalConsent = true
	if _, err := a.SwitchPrivateRuntimeAccount("original", false); err == nil {
		t.Fatal("personal consent bypassed")
	}
	if len(f.start) != 0 {
		t.Fatal("started without personal consent")
	}
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	if f.stops != 0 {
		t.Fatal("stop ran without confirmation")
	}
	a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID)
	awaitControlResult(t, done)
}
func TestRuntimeControlExternalAndZeroJSON(t *testing.T) {
	a, f := controlApp(t)
	f.pending = true
	f.running = 0
	s := controlState(t, a)
	if s.Phase != "external" || !s.Pending || s.CanCancel || s.CanStop || s.RequestID != "" {
		t.Fatalf("external authority: %+v", s)
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"runningTurns":0`) {
		t.Fatalf("zero omitted: %s", b)
	}
	if _, err := a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", "foreign"); err == nil {
		t.Fatal("external cancel accepted")
	}
	a.CancelPrivateRuntimeSwitches()
	if !f.pending {
		t.Fatal("shutdown touched external gate")
	}
}
func TestRuntimeControlActivationCannotBeCanceled(t *testing.T) {
	a, f := controlApp(t)
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	close(f.activate)
	select {
	case <-f.activated:
	case <-time.After(time.Second):
		t.Fatal("activation did not start")
	}
	if got := controlState(t, a).Phase; got != "activating" {
		t.Fatalf("phase after activation = %s, want activating", got)
	}
	if _, err := a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID); err == nil {
		t.Fatal("activation cancel accepted")
	}
	if _, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID, true); err == nil {
		t.Fatal("activation stop accepted")
	}
	a.CancelPrivateRuntimeSwitches()
	close(f.finish)
	if err := awaitControlResult(t, done); err != nil {
		t.Fatal(err)
	}
	if controlState(t, a).Phase != "completed" {
		t.Fatal("shutdown canceled activation")
	}
}
func TestRuntimeControlShutdownCancelsOwnedPending(t *testing.T) {
	a, f := controlApp(t)
	done := beginControlSwitch(t, a, f)
	a.CancelPrivateRuntimeSwitches()
	if !errors.Is(awaitControlResult(t, done), context.Canceled) {
		t.Fatal("pending switch survived shutdown")
	}
	if _, err := a.SwitchPrivateRuntimeAccount("replacement", true); err == nil {
		t.Fatal("switch started after shutdown")
	}
}

func TestRuntimeControlCancelActivationRaceReportsCompletion(t *testing.T) {
	a, f := controlApp(t)
	a.privateSwitch.hooks.switchAccount = func(ctx context.Context, id, exe string, activate func()) error {
		f.start <- struct{}{}
		<-ctx.Done()
		// Simulate cancellation after the backend's final cancellation check.
		activate()
		return nil
	}
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	state, err := a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID)
	if err != nil || state.Phase != "completed" || state.CanCancel {
		t.Fatalf("activation race: %+v %v", state, err)
	}
	if err := awaitControlResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeControlConfirmedStopKeepsOriginalRequest(t *testing.T) {
	a, f := controlApp(t)
	a.privateSwitch.hooks.stop = func(ctx context.Context, h string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if h != "codex" {
			t.Errorf("wrong harness %q", h)
		}
		f.stops++
		f.running = 0
		return nil
	}
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	state, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID, true)
	if err != nil || state.RequestID != s.RequestID || state.TargetAccountID != "original" || state.RunningTurns != 0 {
		t.Fatalf("confirmed stop: %+v %v", state, err)
	}
	close(f.activate)
	close(f.finish)
	if err := awaitControlResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeControlStopErrorKeepsRemainingCount(t *testing.T) {
	a, f := controlApp(t)
	a.privateSwitch.hooks.stop = func(context.Context, string) error { return errors.New("workflow stop failed") }
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	state, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID, true)
	if err == nil || state.RunningTurns != 2 || !state.Pending {
		t.Fatalf("partial stop: %+v %v", state, err)
	}
	a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID)
	awaitControlResult(t, done)
}

func TestRuntimeControlAntigravityDoesNotNeedPersonalConsent(t *testing.T) {
	a := testPrivateRuntime(t)
	// Antigravity now uses private files and reaches bundle validation without consent.
	account, err := NewAccounts().Create("antigravity", "offline consent test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.SwitchPrivateRuntimeAccount(account.ID, false)
	if err == nil || !strings.Contains(err.Error(), "private bundle is not installed") {
		t.Fatalf("expected private bundle validation: %v", err)
	}
	state, err := a.PrivateRuntimeSwitchState(context.Background(), "antigravity")
	if err != nil || state.RequestID != "" || state.CanStop {
		t.Fatalf("unconsented switch registered: %+v %v", state, err)
	}
}

func TestRuntimeControlCanonicalHarness(t *testing.T) {
	a, f := controlApp(t)
	a.privateSwitch.hooks.prepare = func(string, bool) (string, string, error) { return "claude", "offline", nil }
	done := beginControlSwitch(t, a, f)
	state, err := a.PrivateRuntimeSwitchState(context.Background(), "claude-code")
	if err != nil || state.Harness != "claude-code" || state.RequestID == "" {
		t.Fatalf("canonical state: %+v %v", state, err)
	}
	if _, err = a.CancelPrivateRuntimeAccountSwitch(context.Background(), "claude", state.RequestID); err != nil {
		t.Fatal(err)
	}
	awaitControlResult(t, done)
}

func TestRuntimeControlCancelInterruptsConfirmedStop(t *testing.T) {
	a, f := controlApp(t)
	stopping := make(chan struct{})
	a.privateSwitch.hooks.stop = func(ctx context.Context, h string) error {
		close(stopping)
		<-ctx.Done()
		return ctx.Err()
	}
	done := beginControlSwitch(t, a, f)
	s := controlState(t, a)
	stopped := make(chan error, 1)
	go func() {
		_, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID, true)
		stopped <- err
	}()
	select {
	case <-stopping:
	case <-time.After(time.Second):
		t.Fatal("stop did not start")
	}
	canceled := make(chan error, 1)
	go func() {
		_, err := a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", s.RequestID)
		canceled <- err
	}()
	if err := awaitControlResult(t, canceled); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(awaitControlResult(t, stopped), context.Canceled) {
		t.Fatal("stop did not observe cancellation")
	}
	if !errors.Is(awaitControlResult(t, done), context.Canceled) {
		t.Fatal("switch did not observe cancellation")
	}
}

func TestRuntimeControlStopDoesNotBlockAnotherHarness(t *testing.T) {
	a, f := controlApp(t)
	a.privateSwitch.hooks.prepare = func(id string, _ bool) (string, string, error) {
		if id == "other" {
			return "claude-code", "offline", nil
		}
		return "codex", "offline", nil
	}
	stopping := make(chan struct{})
	a.privateSwitch.hooks.stop = func(ctx context.Context, h string) error { close(stopping); <-ctx.Done(); return ctx.Err() }
	done := beginControlSwitch(t, a, f)
	first := controlState(t, a)
	stopped := make(chan error, 1)
	go func() {
		_, err := a.ConfirmPrivateRuntimeAccountSwitch(context.Background(), "codex", first.RequestID, true)
		stopped <- err
	}()
	select {
	case <-stopping:
	case <-time.After(time.Second):
		t.Fatal("stop did not start")
	}
	other := make(chan error, 1)
	go func() { _, err := a.SwitchPrivateRuntimeAccount("other", true); other <- err }()
	select {
	case <-f.start:
	case <-time.After(time.Second):
		t.Fatal("another harness switch waited for stop")
	}
	state, err := a.PrivateRuntimeSwitchState(context.Background(), "claude")
	if err != nil || state.RequestID == "" || state.RequestID == first.RequestID {
		t.Fatalf("other harness request: %+v %v", state, err)
	}
	a.CancelPrivateRuntimeAccountSwitch(context.Background(), "claude", state.RequestID)
	awaitControlResult(t, other)
	a.CancelPrivateRuntimeAccountSwitch(context.Background(), "codex", first.RequestID)
	awaitControlResult(t, done)
	awaitControlResult(t, stopped)
}

func TestRuntimeControlLifetimeCancelsOwnedPending(t *testing.T) {
	a, f := controlApp(t)
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.SetLifetime(lifetime)
	done := beginControlSwitch(t, a, f)
	cancel()
	if err := awaitControlResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("runtime lifetime did not cancel switch: %v", err)
	}
}
