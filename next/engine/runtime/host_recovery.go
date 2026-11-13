package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// recovery is one host's way back after the engine starts.
type recovery struct {
	done chan struct{} // closes when the host has its forwards and this binary, or the engine stops.
	hold time.Time     // Until then the reconciler does not count a box's silence against it. mu guards it.
}

const (
	// restoreTry bounds one pass over a host's forwards, so a stuck ssh cannot end recovery. Each forward stays in the driver,
	// so the next pass carries on from there and even a very slow host gets there.
	restoreTry = 2 * time.Minute
	// restoreHold ends the wait on a host that answers but keeps refusing its forwards: its boxes show as lost, and the
	// retries go on.
	restoreHold = 10 * time.Minute
	// reconnectGrace covers a box agent's reconnect once its socket is back. Its backoff tops out at 10 seconds.
	reconnectGrace = 20 * time.Second

	restoreRetryMin = time.Second
	restoreRetryMax = 30 * time.Second
)

// Restore opens each box's local socket and returns. A deploy can send a host a binary that takes minutes on a relayed link,
// and a host can be down, so each host then comes back by itself in the background, trying until it answers: first the
// forwards that running boxes reconnect through, then this binary, which only a box that starts needs. Nothing a restart
// stops is lost, because the next engine does the same work from the store.
func (b *Boxes) Restore(ctx context.Context) error {
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Host == "" || record.State == contract.BoxDeleted || record.State == contract.BoxFailed {
			continue
		}
		session, err := b.Store.Session(ctx, record.Session)
		if err != nil {
			return err
		}
		if err := b.Sockets.AddBox(record.ID, record.Session, session.Room); err != nil {
			return err
		}
	}
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return err
	}
	hosts = slices.DeleteFunc(hosts, func(host box.Host) bool { return host.State == box.HostRemoved || host.ExecOnly })
	b.mu.Lock()
	if b.recoveries == nil {
		b.recoveries = make(map[contract.HostName]*recovery)
	}
	for _, host := range hosts {
		b.recoveries[host.Name] = &recovery{done: make(chan struct{}), hold: time.Now().Add(restoreHold)}
	}
	b.mu.Unlock()
	for _, host := range hosts {
		b.recovering.Add(2)
		go func() {
			defer b.recovering.Done()
			b.recoverHost(ctx, host.Name)
		}()
		go func() {
			defer b.recovering.Done()
			b.keepTrying(ctx, host.Name, "identify", func(ctx context.Context) error { return b.identify(ctx, host.Name) })
		}()
	}
	return ctx.Err()
}

// WaitRecovered waits for the recovery work to end, which it does when Restore's context does.
func (b *Boxes) WaitRecovered() { b.recovering.Wait() }

func (b *Boxes) recoverHost(ctx context.Context, name contract.HostName) {
	b.mu.Lock()
	state := b.recoveries[name]
	b.mu.Unlock()
	defer close(state.done)
	steps := box.NewStepLog("host " + string(name))
	restored := b.keepTrying(ctx, name, "restore", func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, restoreTry)
		defer cancel()
		return b.restoreHost(ctx, name)
	})
	if !restored {
		return
	}
	steps.Done("restore forwards")
	b.mu.Lock()
	state.hold = time.Now().Add(reconnectGrace)
	b.mu.Unlock()
	if b.keepTrying(ctx, name, "install binary", func(ctx context.Context) error { return b.installHost(ctx, name) }) {
		steps.Done("install binary")
	}
}

// keepTrying runs step until it works or ctx ends, and reports which. It logs an error only when it changes, as a host can
// stay down for days.
func (b *Boxes) keepTrying(ctx context.Context, name contract.HostName, what string, step func(context.Context) error) bool {
	delay := b.retryDelay
	if delay == 0 {
		delay = restoreRetryMin
	}
	last := ""
	for {
		err := step(ctx)
		if err == nil {
			return true
		}
		if ctx.Err() != nil || errors.Is(err, store.ErrNotFound) { // A host removed meanwhile has nothing left to try.
			return false
		}
		if err.Error() != last {
			last = err.Error()
			log.Printf("host %s: %s: %v; retrying", name, what, err)
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return false
		}
		delay = min(delay*2, restoreRetryMax)
	}
}

// restoring reports whether a host's boxes cannot have reconnected yet because the engine has not put their sockets back.
func (b *Boxes) restoring(name contract.HostName) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.recoveries[name]
	return state != nil && time.Now().Before(state.hold)
}

// recovered reports whether a host's recovery has ended, if it has one.
func (b *Boxes) recovered(name contract.HostName) bool {
	b.mu.Lock()
	state := b.recoveries[name]
	b.mu.Unlock()
	if state == nil {
		return true
	}
	select {
	case <-state.done:
		return true
	default:
		return false
	}
}

// waitRecovered waits for a host's recovery, if it has one, and reports false when ctx ended first.
func (b *Boxes) waitRecovered(ctx context.Context, name contract.HostName) bool {
	b.mu.Lock()
	state := b.recoveries[name]
	b.mu.Unlock()
	if state == nil {
		return true
	}
	select {
	case <-state.done:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

// identify records whether a host is the machine the engine runs on.
func (b *Boxes) identify(ctx context.Context, name contract.HostName) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	same, err := b.engineHost(ctx, h)
	if err != nil {
		return err
	}
	return b.Store.SetEngineHost(ctx, name, same)
}

// restoreHost adds the forward of each box on a host and of its shared mount, which outlives the engine and only waits for its
// socket. A box that fails does not stop the rest. It needs no binary: a running box keeps its agent until it next starts.
func (b *Boxes) restoreHost(ctx context.Context, name contract.HostName) error {
	b.mu.Lock()
	restored := b.restored[name]
	b.mu.Unlock()
	if restored {
		return nil
	}
	host, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	var failed []error
	// A relocated engine host can still have boxes bound to its preexisting FUSE mount.
	// Restore its socket too; new boxes on that host still bind the central folder directly.
	if err := host.AddForward(ctx, b.shareForward()); err != nil {
		failed = append(failed, fmt.Errorf("shared folder: %w", err))
	}
	for _, record := range records {
		if record.Host != name || record.State == contract.BoxDeleted || record.State == contract.BoxFailed {
			continue
		}
		if err := host.AddForward(ctx, b.forward(record.ID)); err != nil {
			failed = append(failed, fmt.Errorf("box %s: %w", record.ID, err))
			if ctx.Err() != nil {
				break
			}
		}
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	b.mu.Lock()
	if b.restored == nil {
		b.restored = make(map[contract.HostName]bool)
	}
	b.restored[name] = true
	b.mu.Unlock()
	return nil
}
