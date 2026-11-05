package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// ServiceHosts is what the service reconciler needs of a host beyond admission: its driver and the room images (5.18).
type ServiceHosts interface {
	ServiceDriver(context.Context, contract.HostName) (box.ServiceDriver, error)
	// ServiceRoomImage builds a room's image on the host through the room image path and returns its local tag.
	ServiceRoomImage(ctx context.Context, host contract.HostName, room contract.RoomID, name string) (string, error)
}

// ServiceTimings bound the waits of a deploy. Start is how long the unit has to become active, Stable how long a service with no
// health command must stay active, Health the health gate, Poll the gap between looks, and CheckEvery how long a service host
// check is trusted.
type ServiceTimings struct{ Start, Stable, Health, Poll, CheckEvery time.Duration }

// DefaultServiceTimings are the timings of spec 5.18.
var DefaultServiceTimings = ServiceTimings{Start: 30 * time.Second, Stable: 10 * time.Second, Health: 2 * time.Minute, Poll: time.Second, CheckEvery: time.Minute}

// serviceState is what the reconciler keeps in memory about services: nothing that a restart cannot read again.
type serviceState struct {
	mu      sync.Mutex
	hosts   ServiceHosts
	timings ServiceTimings
	units   map[contract.ServiceID]box.ServiceUnit // from the last heartbeat that named them
	checks  map[contract.HostName]serviceCheck
	synced  map[contract.HostName]bool          // the host's quadlet files were compared with the rows since the engine started
	work    map[contract.HostName]*sync.RWMutex // readers do host work; discard waits for them before cleanup
}

type serviceCheck struct {
	at    time.Time
	check box.ServiceHostCheck
}

// SetServices turns on services before the reconciler runs.
func (r *Reconciler) SetServices(hosts ServiceHosts, timings ServiceTimings) {
	r.svc = &serviceState{hosts: hosts, timings: timings, units: map[contract.ServiceID]box.ServiceUnit{}, checks: map[contract.HostName]serviceCheck{}, synced: map[contract.HostName]bool{}, work: map[contract.HostName]*sync.RWMutex{}}
}

// ObserveServices takes the service states of a host's heartbeat.
func (r *Reconciler) ObserveServices(units []box.ServiceUnit) {
	if r.svc == nil {
		return
	}
	r.svc.mu.Lock()
	for _, u := range units {
		r.svc.units[u.ID] = u
	}
	r.svc.mu.Unlock()
	r.Kick()
}

// ServiceIDs lists the services a host's heartbeat reads.
func (r *Reconciler) ServiceIDs(ctx context.Context, host contract.HostName) []contract.ServiceID {
	if r.svc == nil {
		return nil
	}
	rows, err := r.store.Services(ctx, "")
	if err != nil {
		return nil
	}
	var ids []contract.ServiceID
	for _, row := range rows {
		if row.Host == host && row.Deployed > 0 {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

func (r *Reconciler) serviceEvent(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
	return r.event(room, contract.Principal{Kind: contract.PrincipalEngine}, body)
}

// saveService writes next over old and reports whether old was still current.
func (r *Reconciler) saveService(ctx context.Context, old, next store.ServiceRow, extra ...contract.EventBody) (store.ServiceRow, bool, error) {
	next.Updated = r.now()
	if old.State != next.State {
		next.Since = next.Updated
	}
	reservation := next.Reservation()
	next.CPUMilli, next.MemBytes, next.DiskBytes = reservation.CPUMilli, reservation.MemBytes, reservation.DiskBytes
	saved, err := r.store.SaveService(ctx, old, next, r.serviceEvent, extra...)
	if saved {
		next.Rev = old.Rev + 1
		return next, true, err
	}
	return old, false, err
}

// pendingServiceReasons maps what admission says to what a service shows. Each admission reason has its own.
var pendingServiceReasons = map[box.PendingReason]contract.ServiceReason{
	box.PendingCPU: contract.ServiceNoCPU, box.PendingMemory: contract.ServiceNoMemory, box.PendingDisk: contract.ServiceNoDisk,
	box.PendingRoomHost: contract.ServiceRoomHost, box.PendingNoReadyHost: contract.ServiceRoomHost, box.PendingRoomLimit: contract.ServiceRoomLimit,
}

// servicePass places the services that have no host and starts one step for each placed service.
func (r *Reconciler) servicePass(ctx context.Context, hosts []box.Host, boxes []box.Box) error {
	if r.svc == nil {
		return nil
	}
	rows, err := r.store.Services(ctx, "")
	if err != nil {
		return err
	}
	byName := make(map[contract.HostName]box.Host, len(hosts))
	for _, h := range hosts {
		byName[h.Name] = h
	}
	for _, h := range hosts {
		if h.State == box.HostReady && !h.ExecOnly {
			name := h.Name
			r.launchRoomStep(ctx, "service leftovers "+string(name), func(ctx context.Context) error { return r.repayServiceLeftovers(ctx, name) })
		}
	}
	for i := range rows {
		row := rows[i]
		if row.Host == "" {
			if row.Want == contract.ServiceWantRemove {
				if err := r.store.DeleteService(ctx, row, false); err != nil {
					return err
				}
				continue
			}
			if row.State == contract.ServiceFailed {
				continue
			}
			if err := r.placeService(ctx, &rows[i], hosts, boxes, rows); err != nil {
				return err
			}
			continue
		}
		host, ok := byName[row.Host]
		if !ok {
			continue
		}
		r.launchRoomStep(ctx, "service "+string(row.ID), func(ctx context.Context) error { return r.serviceStep(ctx, row, host) })
	}
	return nil
}

// placeService places one service. A placement is added to the snapshot at once, to hosts as a hold and to all as the row's host, so
// the next service of the same pass is judged with it: the quarter cap and the port rule hold across one pass.
func (r *Reconciler) placeService(ctx context.Context, rowp *store.ServiceRow, hosts []box.Host, boxes []box.Box, all []store.ServiceRow) error {
	row := *rowp
	only, err := r.store.RoomOnly(ctx, row.Room)
	if err != nil {
		return err
	}
	estimate, err := r.store.ImageEstimate(ctx, row.Room, row.Parts)
	if err != nil {
		return err
	}
	limits, err := r.store.RoomLimits(ctx, row.Room)
	if err != nil {
		return err
	}
	holds, err := r.store.ServiceHolds(ctx)
	if err != nil {
		return err
	}
	request := box.ServiceRequest{ID: row.ID, Room: row.Room, Parts: row.Reservation(), ImageEstimate: estimate}
	placement := box.Placement{Reason: box.PendingRoomLimit}
	if box.ServiceFitsRoom(request, limits, boxes, holds) {
		placement = box.AdmitService(hosts, boxes, request, only...)
	}
	next := row
	if placement.Host != "" {
		// A host port another service of that host holds keeps this one waiting, as a clash at set would have refused it.
		taken := slices.ContainsFunc(all, func(other store.ServiceRow) bool {
			return other.ID != row.ID && other.Host == placement.Host && other.Want != contract.ServiceWantRemove && !disjointPorts(other.Reservation().Ports, row.Reservation().Ports)
		})
		if taken {
			next.State, next.Reason = contract.ServicePending, contract.ServicePortTaken
		} else {
			next.Host, next.Placed, next.ImageEstimate = placement.Host, r.now(), estimate
			for i := range hosts {
				if hosts[i].Name == placement.Host {
					hosts[i].Services = append(slices.Clone(hosts[i].Services), box.ServiceHold{ID: row.ID, Room: row.Room, CPUMilli: row.CPUMilli, MemBytes: row.MemBytes, DiskBytes: row.DiskBytes + max(row.ImageBytes, estimate)})
				}
			}
			rowp.Host = placement.Host
			next.State, next.Reason = contract.ServicePending, ""
		}
	} else {
		next.State, next.Reason = contract.ServicePending, pendingServiceReasons[placement.Reason]
	}
	if next.State == row.State && next.Reason == row.Reason && next.Host == row.Host {
		return nil
	}
	if _, _, err := r.saveService(ctx, row, next); err != nil {
		return err
	}
	if next.Host != "" {
		r.Kick()
	}
	return nil
}

func disjointPorts(a, b []contract.PortBinding) bool {
	return !slices.ContainsFunc(a, func(x contract.PortBinding) bool {
		return slices.ContainsFunc(b, func(y contract.PortBinding) bool { return x.Host == y.Host && x.Proto == y.Proto })
	})
}

// serviceHostWork returns the gate shared by all operations that can write or start services on this host. Never hold svc.mu
// while acquiring the gate: a worker takes the gate before reading the service caches.
func (r *Reconciler) serviceHostWork(host contract.HostName) *sync.RWMutex {
	r.svc.mu.Lock()
	defer r.svc.mu.Unlock()
	work := r.svc.work[host]
	if work == nil {
		work = &sync.RWMutex{}
		r.svc.work[host] = work
	}
	return work
}

// currentServiceHost checks placement after entering the host's work gate. A queued step or owner call may still hold the
// pre-discard snapshot. The host must not be draining or removed, and the row must still belong to it, even if the name was re-added.
func (r *Reconciler) currentServiceHost(ctx context.Context, row store.ServiceRow) (box.Host, store.ServiceRow, bool, error) {
	hosts, err := r.store.Hosts(ctx)
	if err != nil {
		return box.Host{}, store.ServiceRow{}, false, err
	}
	for _, host := range hosts {
		if host.Name != row.Host || host.State == box.HostDraining || host.State == box.HostRemoved {
			continue
		}
		current, err := r.store.Service(ctx, row.ID)
		if errors.Is(err, store.ErrNotFound) {
			return box.Host{}, store.ServiceRow{}, false, nil
		}
		if err != nil {
			return box.Host{}, store.ServiceRow{}, false, err
		}
		return host, current, current.Host == row.Host, nil
	}
	return box.Host{}, store.ServiceRow{}, false, nil
}

// serviceStep carries one placed service one step toward what it declares. The reconciler runs at most one per service.
func (r *Reconciler) serviceStep(ctx context.Context, row store.ServiceRow, host box.Host) error {
	work := r.serviceHostWork(row.Host)
	work.RLock()
	defer work.RUnlock()
	// A host can drain after this check, but its cleanup cannot pass this worker.
	host, current, placed, err := r.currentServiceHost(ctx, row)
	if err != nil || !placed {
		return err
	}
	row = current // an owner action may have changed intent or generation while this queued step waited at the gate
	if host.State != box.HostReady {
		// A host that is gone takes its services' state with it, and nothing moves them: a removal is owed instead.
		if row.Want == contract.ServiceWantRemove {
			return r.store.DeleteService(ctx, row, true)
		}
		if row.State == contract.ServiceUnreachable {
			return nil
		}
		next := row
		next.LastState, next.State, next.Reason = row.State, contract.ServiceUnreachable, contract.ServiceHostUnreachable
		_, _, err := r.saveService(ctx, row, next)
		return err
	}
	driver, err := r.svc.hosts.ServiceDriver(ctx, host.Name)
	if err != nil {
		return err
	}
	if row.State == contract.ServiceUnreachable {
		// The host answers again: the state it had comes back, and the next heartbeat gives the real one.
		next := row
		next.State, next.Reason, next.LastState = row.LastState, "", ""
		if next.State == "" {
			next.State = contract.ServicePending
		}
		var ok bool
		if row, ok, err = r.saveService(ctx, row, next); err != nil || !ok {
			return err
		}
	}
	if row.Want == contract.ServiceWantRemove {
		if err := driver.RemoveService(ctx, row.ID); err != nil {
			return fmt.Errorf("service %s: remove: %w", row.ID, err)
		}
		return r.store.DeleteService(ctx, row, false)
	}
	if host.EngineHost == nil || *host.EngineHost {
		// The engine's own host, or one not known not to be: nothing of a service is started there. A removal above still reaches it.
		next := row
		next.State, next.Reason = contract.ServicePending, contract.ServiceRoomHost
		_, _, err := r.saveService(ctx, row, next)
		return err
	}
	check, issues, err := r.serviceHostCheck(ctx, host.Name, driver)
	if err != nil {
		return err
	}
	if len(issues) > 0 {
		next := row
		next.State, next.Reason = contract.ServicePending, contract.ServiceHostCheck
		next.Detail = fmt.Sprint(issues)
		if next.Deployed > 0 {
			return nil // what runs keeps running, and the check is read again in a minute
		}
		_, _, err := r.saveService(ctx, row, next)
		return err
	}
	if err := r.syncServiceHost(ctx, host.Name, driver); err != nil {
		return err
	}
	switch {
	case row.State == contract.ServiceFailed:
		return nil
	case row.Want == contract.ServiceWantStop:
		return r.stopService(ctx, row, driver)
	case row.Deployed < row.Generation:
		return r.deployService(ctx, row, driver, check)
	}
	return r.observeService(ctx, row, driver)
}

func (r *Reconciler) serviceHostCheck(ctx context.Context, host contract.HostName, driver box.ServiceDriver) (box.ServiceHostCheck, []contract.ServiceHostIssue, error) {
	r.svc.mu.Lock()
	cached, ok := r.svc.checks[host]
	r.svc.mu.Unlock()
	if !ok || r.now().Sub(cached.at) >= r.svc.timings.CheckEvery {
		check, err := driver.CheckServiceHost(ctx)
		if err != nil {
			return check, nil, err
		}
		cached = serviceCheck{at: r.now(), check: check}
		r.svc.mu.Lock()
		r.svc.checks[host] = cached
		r.svc.mu.Unlock()
	}
	return cached.check, cached.check.Issues, nil
}

// syncServiceHost runs once for each host after the engine starts. It writes the service slice, leaves a quadlet file with the
// hash the row holds alone, writes again one that is missing or differs, and removes a file with no row.
func (r *Reconciler) syncServiceHost(ctx context.Context, host contract.HostName, driver box.ServiceDriver) error {
	r.svc.mu.Lock()
	done := r.svc.synced[host]
	r.svc.mu.Unlock()
	if done {
		return nil
	}
	if err := driver.EnsureServiceSlice(ctx); err != nil {
		return fmt.Errorf("host %s: service slice: %w", host, err)
	}
	files, err := driver.ListServiceFiles(ctx)
	if err != nil {
		return fmt.Errorf("host %s: list service files: %w", host, err)
	}
	rows, err := r.store.Services(ctx, "")
	if err != nil {
		return err
	}
	onHost := map[contract.ServiceID]store.ServiceRow{}
	for _, row := range rows {
		if row.Host == host && row.Deployed > 0 {
			onHost[row.ID] = row
		}
	}
	found := map[contract.ServiceID]bool{}
	for _, file := range files {
		found[file.ID] = true
		row, ok := onHost[file.ID]
		if !ok {
			// Another host may hold this service's row only if it was removed here while offline, and that debt is repaid.
			if err := driver.RemoveService(ctx, file.ID); err != nil {
				return fmt.Errorf("host %s: remove stray service %s: %w", host, file.ID, err)
			}
			continue
		}
		if file.SHA != row.UnitSHA {
			if err := r.rewriteService(ctx, row, driver); err != nil {
				return err
			}
		}
	}
	for id, row := range onHost {
		if !found[id] && row.Want == contract.ServiceWantRun {
			if err := r.rewriteService(ctx, row, driver); err != nil {
				return err
			}
		}
	}
	r.svc.mu.Lock()
	r.svc.synced[host] = true
	r.svc.mu.Unlock()
	return nil
}

// rewriteService puts a service's quadlet file back as its row says and starts the unit.
func (r *Reconciler) rewriteService(ctx context.Context, row store.ServiceRow, driver box.ServiceDriver) error {
	// What runs is what the last good deploy wrote, and not a newer declaration that is waiting or failed, so the file and its hash
	// stay the same until a deploy succeeds.
	parts := row.Parts
	if row.Live != nil {
		parts = *row.Live
	}
	values, err := r.store.ServiceSecretValues(ctx, row.ID)
	if err != nil {
		return err
	}
	for _, name := range parts.Spec.Secrets {
		if value, ok := values[name]; ok {
			if err := driver.WriteServiceSecret(ctx, row.ID, name, value); err != nil {
				return fmt.Errorf("service %s: write secret %s: %w", row.ID, name, err)
			}
		}
	}
	if _, err := driver.WriteServiceUnit(ctx, box.ServiceUnitSpec{ID: row.ID, Room: row.Room, Parts: parts, Image: row.ImageRef}); err != nil {
		return fmt.Errorf("service %s: write unit: %w", row.ID, err)
	}
	if row.Want == contract.ServiceWantRun {
		if err := driver.RestartService(ctx, row.ID); err != nil {
			return fmt.Errorf("service %s: start: %w", row.ID, err)
		}
	}
	return nil
}

func (r *Reconciler) stopService(ctx context.Context, row store.ServiceRow, driver box.ServiceDriver) error {
	if row.State == contract.ServiceStopped {
		r.svc.mu.Lock()
		unit, seen := r.svc.units[row.ID]
		r.svc.mu.Unlock()
		if !seen || unit.State == box.UnitStopped || unit.State == box.UnitAbsent {
			return nil
		}
		// A reboot can start WantedBy=default.target units. The heartbeat supersedes the old stopped row.
	}
	if err := driver.StopService(ctx, row.ID); err != nil {
		return fmt.Errorf("service %s: stop: %w", row.ID, err)
	}
	if row.State == contract.ServiceStopped {
		return nil
	}
	next := row
	next.State, next.Reason, next.Cause = contract.ServiceStopped, "", ""
	_, _, err := r.saveService(ctx, row, next)
	return err
}

// observeService takes the state the heartbeat read and makes it the row's. A crash loop emits its failure once, as the row
// enters it.
func (r *Reconciler) observeService(ctx context.Context, row store.ServiceRow, driver box.ServiceDriver) error {
	r.svc.mu.Lock()
	unit, seen := r.svc.units[row.ID]
	r.svc.mu.Unlock()
	next := row
	var extra []contract.EventBody
	switch {
	case row.State == contract.ServiceStopped && row.Want == contract.ServiceWantRun:
		if err := driver.RestartService(ctx, row.ID); err != nil {
			return fmt.Errorf("service %s: start: %w", row.ID, err)
		}
		next.State, next.Reason = contract.ServiceRunning, ""
	case !seen:
		return nil
	case unit.State == box.UnitAbsent:
		return r.rewriteService(ctx, row, driver)
	case unit.State == box.UnitRunning:
		if row.State != contract.ServiceRunning {
			next.State, next.Reason, next.Cause = contract.ServiceRunning, "", ""
		}
	case unit.State == box.UnitCrashing || unit.State == box.UnitStopped:
		next.State = contract.ServiceCrashing
		if unit.CrashLoop {
			next.Reason = contract.ServiceCrashLoop
		} else {
			next.Reason = ""
		}
		if next.Reason == contract.ServiceCrashLoop && row.Reason != contract.ServiceCrashLoop {
			extra = append(extra, contract.ServiceFailure{Service: row.ID, Name: row.Spec.Name, Host: row.Host, Reason: contract.ServiceCrashLoop})
		}
	}
	next.Restarts = unit.Restarts
	if next.State == row.State && next.Reason == row.Reason && next.Cause == row.Cause && next.Restarts == row.Restarts {
		return nil
	}
	_, _, err := r.saveService(ctx, row, next, extra...)
	return err
}

// deployFailure is the step that failed and why, set where the step failed.
type deployFailure struct {
	reason contract.ServiceReason
	detail string
}

func (f *deployFailure) Error() string { return string(f.reason) + ": " + f.detail }

// deployService runs the steps of 5.18 for a generation: image, unit, start and health. A failure leaves the previous version
// running again if there was one, and the service failed if not.
func (r *Reconciler) deployService(ctx context.Context, row store.ServiceRow, driver box.ServiceDriver, check box.ServiceHostCheck) error {
	hosts, err := r.store.Hosts(ctx)
	if err != nil {
		return err
	}
	boxes, err := r.store.Boxes(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(hosts, func(h box.Host) bool { return h.Name == row.Host })
	if i < 0 {
		return nil
	}
	request := box.ServiceRequest{ID: row.ID, Room: row.Room, Parts: row.Reservation(), ImageEstimate: max(row.ImageBytes, row.ImageEstimate)}
	if row.Target == "" && !box.ServiceFitsDisk(hosts[i], boxes, request) {
		next := row
		next.State, next.Reason = contract.ServicePending, contract.ServiceNoDisk
		if next.State == row.State && next.Reason == row.Reason {
			return nil
		}
		_, _, err := r.saveService(ctx, row, next)
		return err
	}
	values, err := r.store.ServiceSecretValues(ctx, row.ID)
	if err != nil {
		return err
	}
	for _, name := range row.Spec.Secrets {
		if _, ok := values[name]; !ok {
			next := row
			next.Reason, next.Detail = contract.ServiceSecretMissing, name
			if row.Deployed == 0 {
				next.State = contract.ServicePending
			}
			if next.State == row.State && next.Reason == row.Reason && next.Detail == row.Detail {
				return nil
			}
			_, _, err := r.saveService(ctx, row, next)
			return err
		}
	}
	step := func(phase contract.ServiceReason) (store.ServiceRow, bool, error) {
		if row.State == contract.ServiceDeploying && row.Reason == phase {
			return row, true, nil
		}
		next := row
		next.State, next.Reason, next.Cause, next.Detail = contract.ServiceDeploying, phase, "", ""
		if row.Deployed > 0 {
			next.State = row.State // what runs is still what runs, and an update shows its phase through the reason
		}
		return r.saveService(ctx, row, next)
	}
	failure := r.runDeploy(ctx, &row, driver, check, values, step)
	if failure == nil {
		return nil
	}
	var fail *deployFailure
	if !errors.As(failure, &fail) {
		return failure
	}
	return r.failDeploy(ctx, row, driver, fail)
}

// runDeploy runs the four steps. It returns a *deployFailure for a step that failed, or another error for a step that could not run.
func (r *Reconciler) runDeploy(ctx context.Context, row *store.ServiceRow, driver box.ServiceDriver, check box.ServiceHostCheck, values map[string][]byte, step func(contract.ServiceReason) (store.ServiceRow, bool, error)) error {
	var ok bool
	var err error
	if *row, ok, err = step(contract.ServicePhaseImage); err != nil || !ok {
		return err
	}
	ref := row.Target
	if ref == "" {
		if row.Parts.Digest != "" {
			ref = row.Parts.Digest
			if err := driver.PullService(ctx, ref); err != nil {
				return &deployFailure{contract.ServiceImagePull, err.Error()}
			}
		} else if ref, err = r.svc.hosts.ServiceRoomImage(ctx, row.Host, row.Room, row.Parts.Spec.Image); err != nil {
			return &deployFailure{contract.ServiceImageBuild, err.Error()}
		}
	}
	size, err := driver.ImageSize(ctx, ref)
	if err != nil {
		// The image is not on the host: pulled and built ones were just made, so this is a kept image that is gone.
		reason := contract.ServiceImageBuild
		if row.Parts.Digest != "" || row.Target != "" {
			reason = contract.ServiceImagePull
		}
		return &deployFailure{reason, err.Error()}
	}
	if row.Target == "" {
		if err := r.store.RecordServiceImage(ctx, row.Room, row.Parts, size, r.now()); err != nil {
			return err
		}
	}
	row.ImageBytes = max(row.ImageBytes, size)
	if *row, ok, err = step(contract.ServicePhaseUnit); err != nil || !ok {
		return err
	}
	for _, port := range row.Parts.Ports {
		if port.Host < check.LowestPort {
			return &deployFailure{contract.ServicePortPrivileged, fmt.Sprintf("host port %d is below the host's lowest unprivileged port %d", port.Host, check.LowestPort)}
		}
	}
	listening, err := driver.ListeningPorts(ctx)
	if err != nil {
		return fmt.Errorf("service %s: listening ports: %w", row.ID, err)
	}
	services, err := r.store.Services(ctx, "")
	if err != nil {
		return err
	}
	for _, port := range row.Parts.Ports {
		heldByService := slices.ContainsFunc(services, func(s store.ServiceRow) bool {
			return s.Host == row.Host && s.Deployed > 0 && !disjointPorts(s.Reservation().Ports, []contract.PortBinding{port})
		})
		if !heldByService && slices.ContainsFunc(listening, func(l contract.PortBinding) bool { return l.Host == port.Host && l.Proto == port.Proto }) {
			return &deployFailure{contract.ServicePortInUse, fmt.Sprintf("host port %d/%s is held by a process that is not a Dyna service", port.Host, port.Proto)}
		}
	}
	for _, name := range row.Spec.Secrets {
		if err := driver.WriteServiceSecret(ctx, row.ID, name, values[name]); err != nil {
			return &deployFailure{contract.ServiceUnitFailed, fmt.Sprintf("secret %s could not be written to the host", name)}
		}
	}
	sha, err := driver.WriteServiceUnit(ctx, box.ServiceUnitSpec{ID: row.ID, Room: row.Room, Parts: row.Parts, Image: ref})
	if err != nil {
		return &deployFailure{contract.ServiceUnitFailed, err.Error()}
	}
	if *row, ok, err = step(contract.ServicePhaseStart); err != nil || !ok {
		return err
	}
	if err := driver.RestartService(ctx, row.ID); err != nil {
		return &deployFailure{contract.ServiceStartFailed, err.Error()}
	}
	if !r.waitFor(ctx, r.svc.timings.Start, func() bool {
		unit, err := driver.ShowService(ctx, row.ID)
		return err == nil && unit.State == box.UnitRunning
	}) {
		return &deployFailure{contract.ServiceStartFailed, "the unit did not become active"}
	}
	if *row, ok, err = step(contract.ServicePhaseHealth); err != nil || !ok {
		return err
	}
	if len(row.Spec.Health) > 0 {
		if !r.waitFor(ctx, r.svc.timings.Health, func() bool { return driver.ServiceHealthy(ctx, row.ID, row.Spec.Health) == nil }) {
			return &deployFailure{contract.ServiceHealthFailed, "the health command did not succeed in time"}
		}
	} else if !r.staysActive(ctx, row.ID, driver) {
		return &deployFailure{contract.ServiceHealthFailed, "the unit did not stay active"}
	}
	next := *row
	if row.ImageRef != "" && row.ImageRef != ref {
		next.Previous = row.ImageRef
	}
	live := row.Parts
	next.Live = &live
	next.ImageBytes, next.ImageEstimate = size, 0
	next.ImageRef, next.Target, next.UnitSHA, next.Deployed = ref, "", sha, row.Generation
	next.State, next.Reason, next.Cause, next.Detail, next.LastState = contract.ServiceRunning, "", "", "", ""
	*row, _, err = r.saveService(ctx, *row, next)
	return err
}

// waitFor polls until ok is true, the wait runs out or the context ends.
func (r *Reconciler) waitFor(ctx context.Context, limit time.Duration, ok func() bool) bool {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(max(r.svc.timings.Poll, time.Millisecond))
	defer tick.Stop()
	for {
		if ok() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

// staysActive says whether the unit stays active for the stable window, looking each poll.
func (r *Reconciler) staysActive(ctx context.Context, id contract.ServiceID, driver box.ServiceDriver) bool {
	end := time.NewTimer(r.svc.timings.Stable)
	defer end.Stop()
	tick := time.NewTicker(max(r.svc.timings.Poll, time.Millisecond))
	defer tick.Stop()
	for {
		unit, err := driver.ShowService(ctx, id)
		if err != nil || unit.State != box.UnitRunning {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-end.C:
			return true
		case <-tick.C:
		}
	}
}

// failDeploy ends a deploy that a step failed. With a version that ran before, its quadlet is written back with its image and
// the unit restarted: the service runs with rolled_back and the step's reason as cause. With none it is failed. Either way one
// service.failed event carries the typed reason.
func (r *Reconciler) failDeploy(ctx context.Context, row store.ServiceRow, driver box.ServiceDriver, fail *deployFailure) error {
	next := row
	next.Detail, next.Target = fail.detail, ""
	failure := contract.ServiceFailure{Service: row.ID, Name: row.Spec.Name, Host: row.Host, Detail: fail.detail}
	if row.Deployed == 0 || row.ImageRef == "" {
		next.State, next.Reason, next.Cause = contract.ServiceFailed, fail.reason, ""
		failure.Reason = fail.reason
	} else {
		if err := r.rewriteService(ctx, row, driver); err != nil {
			next.State, next.Reason, next.Cause = contract.ServiceFailed, contract.ServiceStartFailed, fail.reason
			failure.Reason, failure.Cause = contract.ServiceStartFailed, fail.reason
			log.Printf("lifecycle: service %s: roll back: %v", row.ID, err)
		} else {
			next.State, next.Reason, next.Cause = contract.ServiceRunning, contract.ServiceRolledBack, fail.reason
			next.Deployed = row.Generation // this generation is over; a new one starts with the next update
			failure.Reason, failure.Cause = contract.ServiceRolledBack, fail.reason
		}
	}
	_, _, err := r.saveService(ctx, row, next, failure)
	return err
}

// RemoveService asks for a service's removal: the step that owns it deletes it from its host and then its row.
func (r *Reconciler) RemoveService(ctx context.Context, id contract.ServiceID) error {
	_, err := r.store.ChangeService(ctx, id, r.serviceEvent, func(row *store.ServiceRow) error {
		row.Want, row.State, row.Reason = contract.ServiceWantRemove, contract.ServiceRemoving, ""
		return nil
	})
	r.Kick()
	return err
}

// removeRoomServices starts the removal of every service of a deleted room and reports while any is left, so the room's
// deletion waits for their hosts to be clean.
func (r *Reconciler) removeRoomServices(ctx context.Context, room contract.RoomID) error {
	if r.svc == nil {
		return nil
	}
	rows, err := r.store.RoomServices(ctx, room)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Want != contract.ServiceWantRemove {
			if err := r.RemoveService(ctx, row.ID); err != nil {
				return err
			}
		}
	}
	if len(rows) > 0 {
		return fmt.Errorf("%d services are still being removed", len(rows))
	}
	return nil
}

// ErrNoPrevious says a service has no image kept to roll back to.
var ErrNoPrevious = errors.New("the service has no previous image to roll back to")

// ErrNotPlaced says a service has no host yet, so nothing runs to act on.
var ErrNotPlaced = errors.New("the service has no host yet")

// ErrServiceEngineHost refuses a direct restart until the host is known not to run the engine.
var ErrServiceEngineHost = errors.New("the service's host runs the engine or has not yet been checked not to")

// ErrServiceRemoving refuses an owner intent whose service began removal after the caller resolved its ID.
var ErrServiceRemoving = errors.New("the service is being removed")

// reopen lets a failed service try again: a new generation, an update, a start or a restart ends the failure.
func reopen(row *store.ServiceRow) {
	if row.State == contract.ServiceFailed {
		row.State, row.Reason, row.Cause, row.Detail = contract.ServicePending, "", "", ""
	}
}

// StopService, StartService, UpdateService and RollbackService change what a service wants. The service step does the rest, and
// the call returns the row as written.
func (r *Reconciler) StopService(ctx context.Context, id contract.ServiceID) (store.ServiceRow, error) {
	return r.changeService(ctx, id, func(row *store.ServiceRow) error {
		row.Want = contract.ServiceWantStop
		return nil
	})
}

func (r *Reconciler) StartService(ctx context.Context, id contract.ServiceID) (store.ServiceRow, error) {
	return r.changeService(ctx, id, func(row *store.ServiceRow) error {
		row.Want = contract.ServiceWantRun
		reopen(row)
		return nil
	})
}

func (r *Reconciler) UpdateService(ctx context.Context, id contract.ServiceID) (store.ServiceRow, error) {
	return r.changeService(ctx, id, func(row *store.ServiceRow) error {
		row.Generation++
		row.Target = ""
		reopen(row)
		return nil
	})
}

func (r *Reconciler) RollbackService(ctx context.Context, id contract.ServiceID) (store.ServiceRow, error) {
	return r.changeService(ctx, id, func(row *store.ServiceRow) error {
		if row.Previous == "" {
			return ErrNoPrevious
		}
		row.Generation++
		row.Target = row.Previous
		reopen(row)
		return nil
	})
}

func (r *Reconciler) changeService(ctx context.Context, id contract.ServiceID, change func(*store.ServiceRow) error) (store.ServiceRow, error) {
	current, err := r.store.Service(ctx, id)
	if err != nil {
		return current, err
	}
	unlock, err := r.LockServiceDeclaration(ctx, current.Room, current.Spec.Name)
	if err != nil {
		return current, err
	}
	defer unlock()
	row, err := r.store.ChangeService(ctx, id, r.serviceEvent, func(current *store.ServiceRow) error {
		if current.Want == contract.ServiceWantRemove {
			return ErrServiceRemoving
		}
		return change(current)
	})
	r.Kick()
	return row, err
}

// LockServiceDeclaration waits for host work before changing a declaration, then holds admission through its reservation write.
// A worker already past its CAS may still start the previous desired generation, so dropping that generation's resources before
// it finishes would under-reserve the host. Never wait for host work while holding admission: Pass launches workers under it.
// Placement is re-read after both locks, including when the first read had no host and a pass placed it meanwhile.
func (r *Reconciler) LockServiceDeclaration(ctx context.Context, room contract.RoomID, name contract.ServiceName) (func(), error) {
	for {
		row, err := r.store.ServiceNamed(ctx, room, name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		var work *sync.RWMutex
		if r.svc != nil && row.Host != "" {
			work = r.serviceHostWork(row.Host)
			work.Lock()
		}
		unlock := r.store.LockAdmission()
		current, err := r.store.ServiceNamed(ctx, room, name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			unlock()
			if work != nil {
				work.Unlock()
			}
			return nil, err
		}
		if current.Host != row.Host {
			unlock()
			if work != nil {
				work.Unlock()
			}
			continue
		}
		return func() {
			unlock()
			if work != nil {
				work.Unlock()
			}
		}, nil
	}
}

// RestartServiceUnit restarts the unit of a placed service and ends a crash loop: the unit's start limit is reset first.
func (r *Reconciler) RestartServiceUnit(ctx context.Context, row store.ServiceRow) (store.ServiceRow, error) {
	if row.Host == "" {
		return row, ErrNotPlaced
	}
	work := r.serviceHostWork(row.Host)
	work.Lock()
	defer work.Unlock()
	host, current, placed, err := r.currentServiceHost(ctx, row)
	if err != nil {
		return row, err
	}
	if !placed {
		return row, ErrNotPlaced
	}
	row = current
	if row.Want == contract.ServiceWantRemove {
		return row, ErrServiceRemoving
	}
	if host.EngineHost == nil || *host.EngineHost {
		return row, ErrServiceEngineHost
	}
	driver, err := r.svc.hosts.ServiceDriver(ctx, row.Host)
	if err != nil {
		return row, err
	}
	if err := driver.RestartService(ctx, row.ID); err != nil {
		return row, fmt.Errorf("service %s: restart: %w", row.ID, err)
	}
	updated, err := r.store.ChangeService(ctx, row.ID, r.serviceEvent, func(next *store.ServiceRow) error {
		if next.Want == contract.ServiceWantRemove {
			return ErrServiceRemoving
		}
		next.Want = contract.ServiceWantRun
		if next.State == contract.ServiceStopped {
			next.State = contract.ServiceRunning
		}
		if next.State == contract.ServiceCrashing || next.State == contract.ServiceRunning {
			next.Reason = ""
		}
		return nil
	})
	r.Kick()
	return updated, err
}

// ServiceLogs reads a placed service's journal.
func (r *Reconciler) ServiceLogs(ctx context.Context, row store.ServiceRow, lines int, since time.Duration) (string, error) {
	if row.Host == "" {
		return "", ErrNotPlaced
	}
	driver, err := r.svc.hosts.ServiceDriver(ctx, row.Host)
	if err != nil {
		return "", err
	}
	return driver.ServiceLogs(ctx, row.ID, lines, since)
}

// ServiceHostStatus reads the last service host check of a host, for host.list. It is nil for a host not yet checked.
func (r *Reconciler) ServiceHostStatus(host contract.HostName) *contract.ServiceHostStatus {
	if r.svc == nil {
		return nil
	}
	r.svc.mu.Lock()
	defer r.svc.mu.Unlock()
	cached, ok := r.svc.checks[host]
	if !ok {
		return nil
	}
	return &contract.ServiceHostStatus{Issues: cached.check.Issues, LowestPort: cached.check.LowestPort}
}

// repayServiceLeftovers removes from a host what services removed while it was away left there. It runs on every pass for a ready
// host, so a host that comes back is cleaned at once and not at the next engine start.
func (r *Reconciler) repayServiceLeftovers(ctx context.Context, host contract.HostName) error {
	debts, err := r.store.ServiceLeftovers(ctx, host)
	if err != nil || len(debts) == 0 {
		return err
	}
	driver, err := r.svc.hosts.ServiceDriver(ctx, host)
	if err != nil {
		return err
	}
	for _, id := range debts {
		if err := driver.RemoveService(ctx, id); err != nil {
			return fmt.Errorf("host %s: remove service %s: %w", host, id, err)
		}
		if err := r.store.ForgetServiceLeftover(ctx, host, id); err != nil {
			return err
		}
	}
	return nil
}

// removeServiceFromHost removes a service's unit, secrets and volume from its host, as far as the host answers. A failure is logged,
// since the service's row goes on regardless and its removal is owed instead.
func (r *Reconciler) removeServiceFromHost(ctx context.Context, row store.ServiceRow) {
	driver, err := r.svc.hosts.ServiceDriver(ctx, row.Host)
	if err == nil {
		err = driver.RemoveService(ctx, row.ID)
	}
	if err != nil {
		log.Printf("host %s: discard service %s: %v", row.Host, row.ID, err)
		_ = r.store.AddServiceLeftover(ctx, row.Host, row.Room, row.ID)
	}
}
