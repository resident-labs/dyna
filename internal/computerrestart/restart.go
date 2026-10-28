// Package computerrestart decides when the computer service may restart
// without disturbing users, and finishes restarts that an install deferred.
package computerrestart

import (
	"context"
	"errors"
	"time"

	"dyna-agent/internal/computers"
	"dyna-agent/internal/userservices"
)

// Quiet is the preliminary quiet check used to avoid a restart request when
// active use is already known. BeginRestart makes the final decision atomic.
func Quiet(list []computers.Status) string {
	for _, st := range list {
		if reason := st.RestartReason(); reason != "" {
			return reason
		}
	}
	return ""
}

// RestartNow atomically closes foreground command admission and restarts the
// service when quiet. An older daemon counts as busy because its legacy
// commands have no leases and cannot be counted safely.
func RestartNow(ctx context.Context) (string, error) {
	admission, err := computers.NewClient("").BeginRestart(ctx)
	if err != nil {
		if errors.Is(err, computers.ErrRestartAdmissionUnsupported) {
			return "computer daemon predates protected foreground commands; stop it after existing commands finish", nil
		}
		if active, aerr := userservices.ComputersActive(ctx); aerr == nil && !active {
			return "", nil
		}
		return "computer activity is unknown: " + err.Error(), nil
	}
	if admission.Reason != "" {
		return admission.Reason, nil
	}
	if err := userservices.TryRestartComputers(ctx); err != nil {
		return "", err
	}
	return "", nil
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
	Restart func(context.Context) (string, error)
}

// Live wires Deps to the running daemon and systemd.
func Live(dir string) Deps {
	c := computers.NewClient("")
	return Deps{Dir: dir, Recheck: Recheck, List: func(ctx context.Context) ([]computers.Status, error) { return c.List(ctx, "") },
		Events: c.Subscribe, Active: userservices.ComputersActive, Restart: RestartNow}
}

// Wait finishes a deferred restart. It does nothing without the marker. It
// rechecks on every computer event, and every Recheck while it has no event
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
		reason := Quiet(list)
		if err != nil {
			if active, aerr := d.Active(ctx); aerr != nil || active {
				reason = "computer activity is unknown"
			} else {
				reason = ""
			}
		}
		if reason == "" {
			reason, err = d.Restart(ctx)
			if err != nil {
				return err
			}
			if reason == "" {
				return userservices.ClearRestartPending(d.Dir)
			}
		}
		// Without an event stream, or when the list failed, look again after
		// Recheck. An open stream wakes the loop on every computer event.
		var timer <-chan time.Time
		if events == nil || err != nil {
			after := d.After
			if after == nil {
				after = time.After
			}
			timer = after(d.Recheck)
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
