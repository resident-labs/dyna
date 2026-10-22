// Package computerrestart decides when the computer service may restart
// without disturbing users, and finishes restarts that an install deferred.
package computerrestart

import (
	"context"
	"fmt"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/userservices"
)

// QuietFor is how long a computer must have had no input to count as quiet.
const QuietFor = 15 * time.Minute

// Quiet is the one quiet check. It reads the same statuses that
// "dyna computer list --all --json" shows. It returns "" when no running
// computer has an app open or input in the last QuietFor. Otherwise it returns
// the reason, and retryIn is how long until input alone would stop blocking
// (0 when an app must close first).
func Quiet(list []computers.Status) (reason string, retryIn time.Duration) {
	for _, st := range list {
		if st.State != computers.StateRunning && st.State != computers.StateStarting {
			continue
		}
		if len(st.Apps) > 0 {
			return fmt.Sprintf("computer %s has %d app(s) open", st.SessionID, len(st.Apps)), 0
		}
		if left := QuietFor - time.Duration(st.IdleSeconds)*time.Second; left > 0 {
			reason, retryIn = fmt.Sprintf("computer %s had input in the last %s", st.SessionID, QuietFor), max(retryIn, left)
		}
	}
	return reason, retryIn
}

// QuietNow checks the live daemon. If the service is active but does not answer,
// activity is unknown, which counts as busy.
func QuietNow(ctx context.Context) (string, error) {
	list, err := computers.NewClient("").List(ctx, "")
	if err != nil {
		if active, aerr := userservices.ComputersActive(ctx); aerr == nil && !active {
			return "", nil
		}
		return "computer activity is unknown: " + err.Error(), nil
	}
	reason, _ := Quiet(list)
	return reason, nil
}

// Recheck bounds how long Wait goes without looking when no event or input
// deadline will wake it.
const Recheck = 30 * time.Second

type Deps struct {
	Dir     string
	Recheck time.Duration
	After   func(time.Duration) <-chan time.Time // nil means time.After
	List    func(context.Context) ([]computers.Status, error)
	Events  func(context.Context) (<-chan computers.Event, error)
	Active  func(context.Context) (bool, error)
	Restart func(context.Context) error
}

// Live wires Deps to the running daemon and systemd.
func Live(dir string) Deps {
	c := computers.NewClient("")
	return Deps{Dir: dir, Recheck: Recheck, List: func(ctx context.Context) ([]computers.Status, error) { return c.List(ctx, "") },
		Events: c.Subscribe, Active: userservices.ComputersActive, Restart: userservices.TryRestartComputers}
}

// Wait finishes a deferred restart. It does nothing without the marker. It
// rechecks on every computer event, when input alone blocks at the moment the
// computer would count as quiet, and every Recheck while it has no event
// stream. A lost stream is resubscribed on each pass. An unanswered list
// restarts only if a successful query shows the service inactive. It returns after the restart or
// when ctx ends.
func Wait(ctx context.Context, d Deps) error {
	if !userservices.RestartIsPending(d.Dir) {
		return nil
	}
	var events <-chan computers.Event
	for {
		if events == nil {
			events, _ = d.Events(ctx)
		}
		list, err := d.List(ctx)
		reason, retryIn := Quiet(list)
		if err != nil {
			if active, aerr := d.Active(ctx); aerr != nil || active {
				reason, retryIn = "computer activity is unknown", 0
			} else {
				reason = ""
			}
		}
		if reason == "" {
			if err := d.Restart(ctx); err != nil {
				return err
			}
			return userservices.ClearRestartPending(d.Dir)
		}
		if retryIn == 0 && events == nil || err != nil {
			retryIn = d.Recheck
		}
		var timer <-chan time.Time
		if retryIn > 0 {
			after := d.After
			if after == nil {
				after = time.After
			}
			timer = after(retryIn)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case <-timer:
		}
	}
}
