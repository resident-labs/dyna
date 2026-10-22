package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// PrivateRuntimeSwitchControl describes one harness, including external gate owners.
type PrivateRuntimeSwitchControl struct {
	Harness         string `json:"harness"`
	RequestID       string `json:"requestId"`
	TargetAccountID string `json:"targetAccountId"`
	Phase           string `json:"phase"`
	Pending         bool   `json:"pending"`
	RunningTurns    int    `json:"runningTurns"`
	CanCancel       bool   `json:"canCancel"`
	CanStop         bool   `json:"canStop"`
}

type privateRuntimeSwitch struct {
	control           sync.Mutex
	boundary          sync.Mutex
	id, target, phase string
	cancel            context.CancelFunc
	ctx               context.Context
	done              chan struct{}
}

type privateRuntimeControls struct {
	// mu protects request identity and activation phase.
	mu         sync.Mutex
	closed     bool
	operations map[string]*privateRuntimeSwitch
	hooks      *privateRuntimeControlHooks
}

// Hooks keep control tests offline. Production always uses the account gate.
type privateRuntimeControlHooks struct {
	prepare       func(string, bool) (string, string, error)
	switchAccount func(context.Context, string, string, func()) error
	status        func(string) (bool, int)
	stop          func(context.Context, string) error
	finish        func(string) (PrivateRuntimeSnapshot, error)
}

func (a *Runtime) runtimeControlHooks(ctx context.Context) privateRuntimeControlHooks {
	if a.privateSwitch.hooks != nil {
		return *a.privateSwitch.hooks
	}
	accounts := a.Accounts
	return privateRuntimeControlHooks{
		prepare: func(id string, _ bool) (string, string, error) {
			n, err := accounts.Find(id)
			if err != nil {
				return "", "", err
			}
			b, err := a.Bundles.Resolve(n.Harness, "")
			return n.Harness, b.Executable, err
		},
		switchAccount: accounts.SwitchWithProgress,
		status:        accounts.SwitchStatus,
		stop:          accounts.StopTurns,
		finish: func(h string) (PrivateRuntimeSnapshot, error) {
			if err := a.Regenerate(h); err != nil {
				return PrivateRuntimeSnapshot{}, err
			}
			return a.PrivateRuntimes(ctx)
		},
	}
}

func (a *Runtime) PrivateRuntimeSwitchState(ctx context.Context, harness string) (PrivateRuntimeSwitchControl, error) {
	h, err := CanonicalHarness(harness)
	if err != nil {
		return PrivateRuntimeSwitchControl{}, err
	}
	pending, count := a.runtimeControlHooks(ctx).status(h)
	c := &a.privateSwitch
	c.mu.Lock()
	defer c.mu.Unlock()
	out := PrivateRuntimeSwitchControl{Harness: h, Phase: "idle", Pending: pending, RunningTurns: count}
	if pending {
		out.Phase = "external"
	}
	if op := c.operations[h]; op != nil && !(pending && (op.phase == "completed" || op.phase == "canceled" || op.phase == "failed")) {
		out.RequestID, out.TargetAccountID, out.Phase = op.id, op.target, op.phase
		out.Pending = pending || op.phase == "waiting" || op.phase == "canceling" || op.phase == "activating"
		out.CanCancel = !c.closed && op.phase == "waiting"
		out.CanStop = out.CanCancel && count > 0
	}
	return out, nil
}

func (a *Runtime) SwitchPrivateRuntimeAccount(id string, confirmed bool) (PrivateRuntimeSnapshot, error) {
	hooks := a.runtimeControlHooks(a.lifetimeContext())
	h, exe, err := hooks.prepare(id, confirmed)
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	h, err = CanonicalHarness(h)
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	c := &a.privateSwitch
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PrivateRuntimeSnapshot{}, errors.New("runtime is shutting down")
	}
	if old := c.operations[h]; old != nil {
		if !old.control.TryLock() {
			c.mu.Unlock()
			return PrivateRuntimeSnapshot{}, errors.New("an account switch control is still running")
		}
		select {
		case <-old.done:
			old.control.Unlock()
		default:
			old.control.Unlock()
			c.mu.Unlock()
			return PrivateRuntimeSnapshot{}, errors.New("an account switch is already pending")
		}
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		c.mu.Unlock()
		return PrivateRuntimeSnapshot{}, err
	}
	ctx, cancel := context.WithCancel(a.lifetimeContext())
	op := &privateRuntimeSwitch{id: hex.EncodeToString(token[:]), target: id, phase: "waiting", ctx: ctx, cancel: cancel, done: make(chan struct{})}
	if c.operations == nil {
		c.operations = make(map[string]*privateRuntimeSwitch)
	}
	c.operations[h] = op
	c.mu.Unlock()
	err = hooks.switchAccount(ctx, id, exe, func() {
		op.boundary.Lock()
		defer op.boundary.Unlock()
		c.mu.Lock()
		op.phase = "activating"
		c.mu.Unlock()
	})
	c.mu.Lock()
	op.phase = "completed"
	if err != nil {
		op.phase = "failed"
		if errors.Is(err, context.Canceled) {
			op.phase = "canceled"
		}
	}
	close(op.done)
	c.mu.Unlock()
	cancel()
	if err != nil {
		return PrivateRuntimeSnapshot{}, err
	}
	return hooks.finish(h)
}

func (a *Runtime) ownedRuntimeSwitch(h, requestID string) (*privateRuntimeSwitch, error) {
	c := &a.privateSwitch
	c.mu.Lock()
	defer c.mu.Unlock()
	op := c.operations[h]
	if c.closed || requestID == "" || op == nil || op.id != requestID {
		return nil, errors.New("account switch request is stale or is owned elsewhere")
	}
	if op.phase != "waiting" {
		return nil, errors.New("account switch can no longer be canceled or stopped")
	}
	return op, nil
}

func (a *Runtime) CancelPrivateRuntimeAccountSwitch(ctx context.Context, harness, requestID string) (PrivateRuntimeSwitchControl, error) {
	h, err := CanonicalHarness(harness)
	if err != nil {
		return PrivateRuntimeSwitchControl{}, err
	}
	c := &a.privateSwitch
	c.mu.Lock()
	op := c.operations[h]
	if c.closed || requestID == "" || op == nil || op.id != requestID || op.phase != "waiting" {
		c.mu.Unlock()
		state, _ := a.PrivateRuntimeSwitchState(ctx, h)
		return state, errors.New("account switch request is stale, external, or past cancellation")
	}
	// Cancel does not wait for a confirmed stop's control lock. Its context
	// also interrupts that stop, while the activation result stays authoritative.
	op.phase = "canceling"
	op.cancel()
	c.mu.Unlock()
	<-op.done
	return a.PrivateRuntimeSwitchState(ctx, h)
}

func (a *Runtime) ConfirmPrivateRuntimeAccountSwitch(ctx context.Context, harness, requestID string, stopConfirmed bool) (PrivateRuntimeSwitchControl, error) {
	h, err := CanonicalHarness(harness)
	if err != nil {
		return PrivateRuntimeSwitchControl{}, err
	}
	op, err := a.ownedRuntimeSwitch(h, requestID)
	if err != nil {
		state, _ := a.PrivateRuntimeSwitchState(ctx, h)
		return state, err
	}
	op.control.Lock()
	defer op.control.Unlock()
	if _, err := a.ownedRuntimeSwitch(h, requestID); err != nil {
		state, _ := a.PrivateRuntimeSwitchState(ctx, h)
		return state, err
	}
	if !stopConfirmed {
		state, _ := a.PrivateRuntimeSwitchState(ctx, h)
		return state, errors.New("confirm that running turns should stop")
	}
	// Keep activation at its boundary until stop has finished. This prevents
	// a late stop from affecting turns admitted after activation.
	op.boundary.Lock()
	defer op.boundary.Unlock()
	if _, err := a.ownedRuntimeSwitch(h, requestID); err != nil {
		state, _ := a.PrivateRuntimeSwitchState(ctx, h)
		return state, err
	}
	ctx, cancel := context.WithTimeout(op.ctx, 60*time.Second)
	defer cancel()
	err = a.runtimeControlHooks(ctx).stop(ctx, h)
	state, stateErr := a.PrivateRuntimeSwitchState(ctx, h)
	if err != nil {
		return state, fmt.Errorf("%d running turns remain after stop: %w", state.RunningTurns, err)
	}
	if stateErr != nil {
		return state, stateErr
	}
	if state.RunningTurns > 0 {
		return state, fmt.Errorf("%d running turns still block the account switch", state.RunningTurns)
	}
	return state, nil
}

func (a *Runtime) CancelPrivateRuntimeSwitches() {
	c := &a.privateSwitch
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, op := range c.operations {
		if op.phase == "waiting" {
			op.phase = "canceling"
			op.cancel()
		}
	}
}
