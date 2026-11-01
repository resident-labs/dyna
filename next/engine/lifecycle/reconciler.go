package lifecycle

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const boxDisconnectedReason = "box disconnected"

type eventFunc func(contract.RoomID, contract.Principal, contract.EventBody) (contract.Event, error)

type activeStep struct {
	cancel context.CancelFunc
	want   store.BoxWant
	done   chan struct{}
}

type Reconciler struct {
	hostMemory   func(contract.HostName) int64
	store        *store.Store
	boxes        BoxAdapter
	view         View
	event        eventFunc
	now          func() time.Time
	kick         chan struct{}
	mu           sync.Mutex
	repos        Repositories
	roomHosts    RoomHosts
	roomData     RoomData
	roomSteps    map[string]bool
	active       map[contract.BoxID]*activeStep
	hosts        map[contract.HostName]int
	fresh        map[contract.BoxID]bool
	disconnected map[contract.BoxID]time.Time
	wg           sync.WaitGroup
	pass         sync.Mutex
}

func New(st *store.Store, boxes BoxAdapter, view View, repos Repositories, event func(contract.RoomID, contract.Principal, contract.EventBody) (contract.Event, error), now func() time.Time) *Reconciler {
	return &Reconciler{store: st, boxes: boxes, view: view, repos: repos, event: event, now: now, kick: make(chan struct{}, 1), roomSteps: make(map[string]bool), active: make(map[contract.BoxID]*activeStep), hosts: make(map[contract.HostName]int), fresh: make(map[contract.BoxID]bool), disconnected: make(map[contract.BoxID]time.Time)}
}
func (r *Reconciler) SetHostMemory(read func(contract.HostName) int64) { r.hostMemory = read }

// SetRoomData enables room deletion before the reconciler runs.
func (r *Reconciler) SetRoomData(data RoomData) { r.roomData = data }

// SetRoomHosts adds the host side of room deletion before the reconciler runs.
func (r *Reconciler) SetRoomHosts(hosts RoomHosts) { r.roomHosts = hosts }

func (r *Reconciler) SetRepositories(repos Repositories) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.repos = repos
}

func (r *Reconciler) Connected(id contract.BoxID) {
	r.pass.Lock()
	delete(r.disconnected, id)
	r.pass.Unlock()
	r.Kick()
}
func (r *Reconciler) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}
func (r *Reconciler) Run(ctx context.Context, ticks <-chan time.Time) {
	r.Kick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
		case _, ok := <-ticks:
			if !ok {
				ticks = nil
				continue
			}
		}
		if err := r.Pass(ctx); err != nil && ctx.Err() == nil {
			log.Printf("lifecycle: %v", err)
		}
	}
}
func (r *Reconciler) Wait() { r.wg.Wait() }
func (r *Reconciler) Cancel(id contract.BoxID) {
	r.pass.Lock()
	done := r.cancel(id)
	r.pass.Unlock()
	if done != nil {
		<-done
	}
}
func (r *Reconciler) cancel(id contract.BoxID) <-chan struct{} {
	r.mu.Lock()
	step := r.active[id]
	if step == nil {
		r.mu.Unlock()
		return nil
	}
	step.cancel()
	r.mu.Unlock()
	return step.done
}
func (r *Reconciler) change(ctx context.Context, old, next box.Box, due time.Time, failures int, cause contract.FailureCause) (bool, error) {
	session, err := r.store.Session(ctx, old.Session)
	if err != nil {
		return false, err
	}
	event, err := r.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Cause: cause})
	if err != nil {
		return false, err
	}
	if old.State == contract.BoxCreating && (next.State == contract.BoxStarting || next.State == contract.BoxFailed || next.State == contract.BoxPending) {
		return true, r.store.FinishCreate(ctx, old, next.Image, next.Reason, due, failures, event)
	}
	return r.store.SetBoxLifecycle(ctx, old, next, due, failures, event)
}
func (r *Reconciler) Pass(ctx context.Context) error {
	r.pass.Lock()
	defer r.pass.Unlock()
	if err := r.store.WakeModelRetries(ctx, r.now()); err != nil {
		return err
	}
	rows, err := r.store.LifecycleBoxes(ctx)
	if err != nil {
		return err
	}
	hosts, err := r.store.Hosts(ctx)
	if err != nil {
		return err
	}
	if r.hostMemory != nil {
		for i := range hosts {
			hosts[i].MemUsed = r.hostMemory(hosts[i].Name)
		}
	}
	hostState := make(map[contract.HostName]box.HostState, len(hosts))
	for _, host := range hosts {
		hostState[host.Name] = host.State
		if host.State == box.HostReady && !host.MaintainedAt.After(r.now().Add(-24*time.Hour)) {
			claimed, err := r.store.ClaimHostMaintenance(ctx, host.Name, r.now())
			if err != nil {
				return err
			}
			if claimed {
				r.wg.Add(1)
				go func(name contract.HostName) {
					defer r.wg.Done()
					sizes, err := r.boxes.MaintainHost(ctx, name)
					if err != nil {
						if ctx.Err() == nil {
							log.Printf("host %s: maintenance: %v", name, err)
						}
						return
					}
					if err := r.store.FinishHostMaintenance(ctx, name, sizes, r.now()); err != nil && ctx.Err() == nil {
						log.Printf("host %s: record maintenance: %v", name, err)
					}
				}(host.Name)
			}
		}
	}
	all := make([]box.Box, len(rows))
	for i := range rows {
		all[i] = rows[i].Box
	}
	for i, row := range rows {
		b := row.Box
		if b.State != contract.BoxReady {
			delete(r.disconnected, b.ID)
		} else {
			lost := hostState[b.Host] == box.HostUnreachable
			if r.view.Connected(b.Session) {
				delete(r.disconnected, b.ID)
			} else {
				since, ok := r.disconnected[b.ID]
				if !ok {
					since = r.now()
					r.disconnected[b.ID] = since
				}
				lost = lost || !r.now().Before(since.Add(60*time.Second))
			}
			if lost {
				next := b
				next.State, next.Reason = contract.BoxLost, boxDisconnectedReason
				if hostState[b.Host] == box.HostUnreachable {
					next.Reason = "host unreachable"
				}
				changed, err := r.change(ctx, b, next, time.Time{}, row.Failures, contract.CauseHostLost)
				if err != nil {
					return err
				}
				if changed {
					b, row.Box, rows[i].Box, all[i] = next, next, next, next
				}
			}
		}
		if row.Want == store.WantClose || row.Want == store.WantDiscard {
			r.mu.Lock()
			step := r.active[b.ID]
			if step != nil && step.want != row.Want {
				step.cancel()
			}
			r.mu.Unlock()
			if step == nil {
				if err := r.closePass(ctx, row, hostState[row.Host]); err != nil {
					return err
				}
			}
			continue
		}
		if row.Paused || row.Want != store.WantRun || (b.State != contract.BoxPending && b.State != contract.BoxStopped) || (!row.DueAt.IsZero() && row.DueAt.After(r.now())) {
			continue
		}
		placement := box.Placement{}
		cpus, memory := b.Size.CPUs, b.Size.MemBytes
		for _, placed := range all {
			if placed.ID == b.ID || placed.Room != b.Room || placed.State == contract.BoxPending || placed.State == contract.BoxStopped || placed.State == contract.BoxFailed || placed.State == contract.BoxDeleted {
				continue
			}
			cpus += placed.Size.CPUs
			memory += placed.Size.MemBytes
		}
		if row.Limits.CPUs > 0 && cpus > 4*row.Limits.CPUs || row.Limits.MemBytes > 0 && memory > 4*row.Limits.MemBytes {
			placement.Reason = box.PendingRoomLimit
		} else {
			placement = box.Admit(hosts, all, b)
		}
		next := b
		if placement.Reason != "" {
			next.State, next.Reason = contract.BoxPending, string(placement.Reason)
		} else if b.Image == "" {
			next.State, next.Reason, next.Host = contract.BoxCreating, "", placement.Host
			next.Slice, err = box.SliceName(b.ID)
			if err != nil {
				return err
			}
		} else {
			next.State, next.Reason = contract.BoxStarting, ""
		}
		if next.State == b.State && next.Reason == b.Reason {
			continue
		}
		changed, err := r.change(ctx, b, next, time.Time{}, row.Failures, "")
		if err != nil {
			return err
		}
		if changed {
			rows[i].Box = next
			rows[i].DueAt = time.Time{}
			all[i] = next
			if next.State == contract.BoxCreating && b.Host == "" {
				r.mu.Lock()
				r.fresh[b.ID] = true
				r.mu.Unlock()
			}
		}
	}
	var quietCandidates []contract.SessionID
	for _, row := range rows {
		if row.Want == store.WantRun && row.Box.State == contract.BoxReady && !r.view.OpenTerminal(row.Box.Session) {
			quietCandidates = append(quietCandidates, row.Box.Session)
		}
	}
	stopped, err := r.store.StopQuiet(ctx, quietCandidates, r.now().Add(-10*time.Minute))
	if err != nil {
		return err
	}
	stoppedSet := make(map[contract.SessionID]bool, len(stopped))
	for _, id := range stopped {
		stoppedSet[id] = true
	}
	for i := range rows {
		if stoppedSet[rows[i].Box.Session] {
			rows[i].Want = store.WantStop
		}
	}
	for _, row := range rows {
		if row.Box.State == contract.BoxLost && hostState[row.Host] == box.HostReady && row.Want != store.WantClose && row.Want != store.WantDiscard {
			if row.DueAt.IsZero() || !row.DueAt.After(r.now()) {
				r.launch(ctx, row)
			}
			continue
		}
		if row.Want == store.WantRun && (row.Box.State == contract.BoxCreating || row.Box.State == contract.BoxStarting) || row.Want == store.WantStop && row.Box.State == contract.BoxReady {
			if !row.DueAt.IsZero() && row.DueAt.After(r.now()) {
				continue
			}
			r.launch(ctx, row)
		}
	}
	return r.roomPass(ctx, hosts)
}
func (r *Reconciler) launch(ctx context.Context, row store.LifecycleBox) {
	r.mu.Lock()
	if r.active[row.ID] != nil || r.hosts[row.Host] >= 4 {
		r.mu.Unlock()
		return
	}
	stepCtx, cancel := context.WithCancel(ctx)
	step := &activeStep{cancel: cancel, want: row.Want, done: make(chan struct{})}
	r.active[row.ID] = step
	r.hosts[row.Host]++
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		if err := r.step(stepCtx, row); err != nil && stepCtx.Err() == nil {
			log.Printf("lifecycle step %s: %v", row.ID, err)
		}
		cancel()
		r.mu.Lock()
		delete(r.active, row.ID)
		r.hosts[row.Host]--
		close(step.done)
		r.mu.Unlock()
		r.Kick()
	}()
}
