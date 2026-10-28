//go:build linux

package computers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/callspec"
)

type daemon struct {
	mu         sync.Mutex
	computers  map[string]*supervisor
	options    SupervisorOptions
	ctx        context.Context
	restarting bool
	subMu      sync.Mutex
	subs       map[uint64]subscriber
	nextSub    uint64
}
type subscriber struct {
	session string
	events  chan Event
}

// RunDaemon holds all public endpoints in one process. A flock serializes
// concurrent lazy starts from different harness turns.
func RunDaemon(ctx context.Context, options SupervisorOptions) error {
	if err := os.Setenv(daemonMarker, "1"); err != nil {
		return err
	}
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return fmt.Errorf("XDG_RUNTIME_DIR is required")
	}
	if err := os.MkdirAll(Root(), 0700); err != nil {
		return err
	}
	runtime := filepath.Dir(ControlSocket(""))
	if err := os.MkdirAll(runtime, 0700); err != nil {
		return err
	}
	_ = os.Chmod(runtime, 0700)
	lock, err := os.OpenFile(filepath.Join(runtime, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	setupComputerCgroups()
	stopReaper := startChildReaper()
	defer stopReaper()
	_ = os.Remove(ControlSocket(""))
	listener, err := unixListener(ControlSocket(""))
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(ControlSocket(""))
	d := &daemon{ctx: ctx, options: options, computers: map[string]*supervisor{}, subs: map[uint64]subscriber{}}
	defer func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, s := range d.computers {
			s.cleanup(false)
		}
	}()
	// Recreate registered endpoints after a daemon crash. Destroyed records stay
	// on disk for history but must never regain sockets.
	entries, _ := os.ReadDir(Root())
	for _, entry := range entries {
		if !entry.IsDir() || ValidateID(entry.Name()) != nil {
			continue
		}
		m, err := ReadMeta(entry.Name())
		if err != nil {
			continue
		}
		if st := savedStatus(m.SessionID); st.State == StateDestroyed {
			continue
		}
		if s, err := newComputer(ctx, m.SessionID, m.Room, options, d.emit); err == nil {
			d.computers[m.SessionID] = s
		} else {
			fmt.Fprintf(os.Stderr, "restore computer %s: %v\n", m.SessionID, err)
		}
	}
	return Serve(ctx, listener, d.dispatch)
}
func (d *daemon) emit(e Event) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for _, sub := range d.subs {
		if sub.session == "" || sub.session == e.SessionID {
			select {
			case sub.events <- e:
			default:
			}
		}
	}
}
func (d *daemon) dispatch(ctx context.Context, r Request) (any, error) {
	if r.Op == "act" {
		var err error
		r.Status, err = ActionStatus(r.Actions, r.Status)
		if err != nil {
			return nil, err
		}
	}

	if r.Op == "subscribe" {
		if r.Session != "" {
			if err := ValidateID(r.Session); err != nil {
				return nil, err
			}
		}
		d.subMu.Lock()
		d.nextSub++
		id := d.nextSub
		events := make(chan Event, 128)
		d.subs[id] = subscriber{session: r.Session, events: events}
		d.subMu.Unlock()
		return Subscription{Events: events, Cancel: func() { d.subMu.Lock(); delete(d.subs, id); d.subMu.Unlock() }}, nil
	}
	if r.Op == "version" {
		return callspec.Build, nil
	}
	if r.Op == "restart.begin" {
		return d.beginRestart(), nil
	}
	if r.Op == "list" {
		d.mu.Lock()
		items := make([]*supervisor, 0, len(d.computers))
		for _, s := range d.computers {
			if r.Room == "" || s.meta.Room == r.Room {
				items = append(items, s)
			}
		}
		d.mu.Unlock()
		out := []Status{}
		seen := map[string]bool{}
		for _, s := range items {
			s.mu.Lock()
			out = append(out, s.status())
			seen[s.meta.SessionID] = true
			s.mu.Unlock()
		}
		entries, _ := os.ReadDir(Root())
		for _, entry := range entries {
			if !entry.IsDir() || seen[entry.Name()] || ValidateID(entry.Name()) != nil {
				continue
			}
			m, err := ReadMeta(entry.Name())
			if err != nil || r.Room != "" && m.Room != r.Room {
				continue
			}
			s := savedStatus(m.SessionID)
			if s.SessionID == "" {
				continue
			}
			s.RSSKiB = 0
			s.StreamRSSKiB = 0
			s.Stream = Stream{}
			s.Viewers = 0
			s.Runs = 0
			s.Control = ControlAgent
			out = append(out, s)
		}
		return out, nil
	}
	if err := ValidateID(r.Session); err != nil {
		return nil, err
	}
	if r.Op == "stream.check" {
		if err := CheckStreamInstalled(); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
	d.mu.Lock()
	s := d.computers[r.Session]
	// A saved computer the service has not loaded since a restart can still be kept.
	if r.Op == "keep" && s == nil {
		if _, err := ReadMeta(r.Session); err == nil {
			var err error
			if s, err = newComputer(d.ctx, r.Session, "", d.options, d.emit); err != nil {
				d.mu.Unlock()
				return nil, err
			}
			d.computers[r.Session] = s
		}
	}
	if r.Op == "register" {
		if s == nil {
			var err error
			s, err = newComputer(d.ctx, r.Session, r.Room, d.options, d.emit)
			if err != nil {
				d.mu.Unlock()
				return nil, err
			}
			d.computers[r.Session] = s
		}
		if r.Room != "" && s.meta.Room != r.Room {
			s.mu.Lock()
			s.meta.Room = r.Room
			s.state.Room = r.Room
			err := saveMeta(s.meta)
			s.mu.Unlock()
			if err != nil {
				d.mu.Unlock()
				return nil, err
			}
		}
		m := s.meta
		d.mu.Unlock()
		return m, nil
	}
	if s == nil {
		d.mu.Unlock()
		if r.Op == "destroy" {
			return Stopped{StateDestroyed}, destroyOffline(ctx, r.Session)
		}
		if r.Op == "status" {
			state := savedStatus(r.Session)
			if state.SessionID != "" {
				state.RSSKiB = 0
				state.StreamRSSKiB = 0
				state.Stream = Stream{}
				state.Viewers = 0
				state.Runs = 0
				state.Control = ControlAgent
				return state, nil
			}
			return Status{SessionID: r.Session, Apps: []App{}, Control: ControlAgent, Reason: "no desktop yet"}, nil
		}
		if r.Op == "shot" || r.Op == "act" {
			state := savedStatus(r.Session)
			shot := Shot{CapturedAt: state.ShotCapturedAt, Width: state.Width, Height: state.Height, Message: "no windows yet"}
			path := filepath.Join(Dir(r.Session), "last.png")
			if _, err := os.Stat(path); err == nil {
				shot.Path = path
			}
			if r.Op == "shot" {
				return shot, nil
			}
			width, height := state.Width, state.Height
			if width == 0 {
				width, height = 1280, 800
			}
			if err := ValidateActions(r.Actions, width, height); err != nil {
				return nil, err
			}
			return ActionResult{Shot: shot, Frames: []Frame{}}, nil
		}
		return nil, fmt.Errorf("no desktop yet")
	}
	if r.Op == "destroy" {
		delete(d.computers, r.Session)
		d.mu.Unlock()
		if err := s.cleanup(true); err != nil {
			return nil, err
		}
		return Stopped{StateDestroyed}, nil
	}
	d.mu.Unlock()
	if r.Op == "run.lease" {
		return d.leaseRun(ctx, s, r)
	}
	if r.Op == "open" {
		return d.openApp(ctx, s, r)
	}
	return s.dispatch(ctx, r)
}

// beginRestart checks active use and closes run admission under the same lock
// that leaseRun uses. Once it returns a quiet answer, no new foreground command
// can appear before the caller asks systemd to restart the service.
func (d *daemon) beginRestart() RestartAdmission {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.computers {
		s.mu.Lock()
		reason := s.state.RestartReason()
		if reason == "" && s.appLaunches > 0 {
			reason = fmt.Sprintf("computer %s has %d app launch(es) in progress", s.meta.SessionID, s.appLaunches)
		}
		s.mu.Unlock()
		if reason != "" {
			return RestartAdmission{Reason: reason}
		}
	}
	d.restarting = true
	return RestartAdmission{}
}

// openApp holds a launch lease from admission until the window appears or
// the launch fails. That closes the gap where an in-progress launch has no app
// in status yet. Once restart admission closes, no new launch can start.
func (d *daemon) openApp(ctx context.Context, s *supervisor, r Request) (Opened, error) {
	d.mu.Lock()
	if d.restarting {
		d.mu.Unlock()
		return Opened{}, fmt.Errorf("computer service restart is in progress; retry after it restarts")
	}
	if d.computers[s.meta.SessionID] != s {
		d.mu.Unlock()
		return Opened{}, fmt.Errorf("computer was destroyed while preparing the app")
	}
	s.mu.Lock()
	s.appLaunches++
	s.record("launches", nil)
	s.mu.Unlock()
	d.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.appLaunches--
		s.record("launches", nil)
		s.mu.Unlock()
	}()
	return s.openApp(ctx, r)
}

func (d *daemon) leaseRun(ctx context.Context, s *supervisor, r Request) (Meta, error) {
	if _, err := s.prepareRun(ctx, r); err != nil {
		return Meta{}, err
	}
	if err := ctx.Err(); err != nil {
		return Meta{}, err
	}
	connection := ConnectionID(ctx)
	d.mu.Lock()
	if d.restarting {
		d.mu.Unlock()
		return Meta{}, fmt.Errorf("computer service restart is in progress; retry after it restarts")
	}
	if d.computers[s.meta.SessionID] != s {
		d.mu.Unlock()
		return Meta{}, fmt.Errorf("computer was destroyed while preparing the command")
	}
	s.mu.Lock()
	s.runs[connection] = struct{}{}
	s.state.Runs = len(s.runs)
	meta := s.meta
	s.record("runs", nil)
	s.mu.Unlock()
	d.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.runs, connection)
		s.state.Runs = len(s.runs)
		s.record("runs", nil)
		s.mu.Unlock()
	}()
	return meta, nil
}
