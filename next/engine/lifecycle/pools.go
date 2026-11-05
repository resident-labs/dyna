package lifecycle

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// Dyna decides when a node is needed, and a pool's script knows how to get one. A node is an ordinary row in hosts, so this step
// only grows and shrinks pools. Its row says where a node is, so a restart simply carries on: provisioning was cut short and
// is undone, and draining is finished.

const (
	// poolAuditEvery is how often each pool's list runs.
	poolAuditEvery = 10 * time.Minute
	// destroyProblem starts a problem that a later destroy can end.
	destroyProblem = "destroy "
	problemBytes   = 2 << 10
)

// Nodes gets machines and gives them back. Create, Destroy and List are the pool's script, and Script answers them. Join runs
// the host check and host setup on a machine, and Leave drops the engine's side of one.
type Nodes interface {
	Create(ctx context.Context, pool string, name contract.HostName, size contract.BoxSizeName) (string, error)
	Destroy(ctx context.Context, pool string, name contract.HostName) error
	List(ctx context.Context, pool string) ([]string, error)
	Join(ctx context.Context, name contract.HostName, ssh string) (box.Host, error)
	Leave(ctx context.Context, name contract.HostName)
}

// Runners is the CI pool as admission and the node pools see it. A runner on a host counts against it as a box does and holds
// a node, and a job that no host takes waits for a node of its room's pool, as a box does.
type Runners interface {
	// Runners returns the runners on hosts, as boxes, and the jobs that wait for a node, as pending boxes.
	Runners() (placed []box.Box, waiting []store.LifecycleBox)
	// Hold runs drain unless a runner is on the host, and starts no runner meanwhile.
	Hold(host contract.HostName, drain func() error) error
}

// SetRunners shows the CI pool's runners to admission and the node pools, before the reconciler runs.
func (r *Reconciler) SetRunners(runners Runners) { r.runners = runners }

// runnerView returns the runners as admission counts them, and as rows the node pools grow for and keep their nodes for.
func (r *Reconciler) runnerView() ([]box.Box, []store.LifecycleBox) {
	if r.runners == nil {
		return nil, nil
	}
	placed, waiting := r.runners.Runners()
	rows := waiting
	for _, b := range placed {
		rows = append(rows, store.LifecycleBox{Box: b})
	}
	return placed, rows
}

// RunnerBoxes returns the runner reservations for an admission decision. The caller holds the store's admission lock until its
// reservation is written, so a CI claim cannot spend the same capacity after this read.
func (r *Reconciler) RunnerBoxes() []box.Box {
	placed, _ := r.runnerView()
	return placed
}

// RunnerCounts says how many CI runners each host has now, as admission counts them.
func (r *Reconciler) RunnerCounts() map[contract.HostName]int {
	placed, _ := r.runnerView()
	counts := make(map[contract.HostName]int)
	for _, b := range placed {
		counts[b.Host]++
	}
	return counts
}

// drainHost closes a host to admission once nothing holds it, box or runner.
func (r *Reconciler) drainHost(ctx context.Context, name contract.HostName) error {
	if r.runners == nil {
		return r.store.DrainHost(ctx, name)
	}
	return r.runners.Hold(name, func() error { return r.store.DrainHost(ctx, name) })
}

// candidates are the hosts a row that waits may use. A CI runner takes only hosts with ci on.
func candidates(row store.LifecycleBox, hosts []box.Host) []box.Host {
	if row.Image == box.RunnerImage {
		return box.RunnerHosts(hosts)
	}
	return hosts
}

// poolDelay is the wait after a failure: a minute, doubling to an hour, as a failing create may cost money each time.
func poolDelay(failures int) time.Duration {
	return min(time.Minute<<min(failures-1, 6), time.Hour)
}

// poolWait says what a new box waits for. In a room whose rule names a pool, that is a node, unless the box asked for a GPU, which
// a new node does not bring.
func poolWait(row store.LifecycleBox, reason box.PendingReason, pools []store.Pool) box.PendingReason {
	if row.Host != "" || row.GPU > 0 || reason == box.PendingRoomLimit || reason == box.PendingInvalidSize {
		return reason
	}
	for _, pool := range pools {
		if slices.Contains(row.Only, pool.Name) {
			if pool.Problem != "" {
				return box.PendingPoolFailed
			}
			return box.PendingPoolNode
		}
	}
	return reason
}

func (r *Reconciler) poolPass(ctx context.Context, pools []store.Pool, rows []store.LifecycleBox, all []box.Box) error {
	if r.nodes == nil || len(pools) == 0 {
		return nil
	}
	nodes, err := r.store.PoolNodes(ctx)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		own := slices.DeleteFunc(slices.Clone(nodes), func(node box.Host) bool { return node.Pool != pool.Name })
		if err := r.shrink(ctx, pool, own, rows); err != nil {
			return err
		}
		if err := r.grow(ctx, pool, own, rows, all); err != nil {
			return err
		}
		r.audit(ctx, pool, own)
	}
	return nil
}

func nodeStep(name contract.HostName) string { return "node " + string(name) }

// grow adds a node for each box that waits for one, up to max. Nodes of a shared pool come one at a time, because the first
// may well take every box that waits.
func (r *Reconciler) grow(ctx context.Context, pool store.Pool, own []box.Host, rows []store.LifecycleBox, all []box.Box) error {
	if pool.RetryAt.After(r.now()) {
		return nil
	}
	count, creating := len(own), 0
	var ready []box.Host
	for _, node := range own {
		switch node.State {
		case box.HostProvisioning:
			creating++
		case box.HostReady:
			ready = append(ready, node)
		}
	}
	for _, row := range rows {
		if count >= pool.Max || !pool.PerSession && creating > 0 {
			return nil
		}
		if !waitsForNode(row, pool) || slices.ContainsFunc(own, func(node box.Host) bool { return node.Box == row.ID }) {
			continue
		}
		if box.Admit(candidates(row, ready), all, row.Box, row.Only...).Host != "" {
			continue // A node came up since this pass read its hosts, and the next pass places the box.
		}
		name, err := nodeName(pool.Name)
		if err != nil {
			return err
		}
		node := box.Host{Name: name, Pool: pool.Name}
		if pool.PerSession {
			node.Box, node.Rooms = row.ID, []contract.RoomID{row.Room}
		}
		started, err := r.store.StartPoolNode(ctx, pool, node)
		if err != nil {
			return err
		}
		if !started {
			continue
		}
		count++
		creating++
		r.launchRoomStep(ctx, nodeStep(name), func(ctx context.Context) error { return r.createNode(ctx, pool, node, row) })
	}
	return nil
}

func waitsForNode(row store.LifecycleBox, pool store.Pool) bool {
	return row.Want == store.WantRun && !row.Paused && row.State == contract.BoxPending && slices.Contains(row.Only, pool.Name) &&
		(row.Reason == string(box.PendingPoolNode) || row.Reason == string(box.PendingPoolFailed))
}

func nodeName(pool string) (contract.HostName, error) {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return contract.HostName(pool + "-" + hex.EncodeToString(raw[:])), nil
}

// createNode gets a machine and makes it a host. A node that cannot serve the box it came for is a failure: it would cost money
// and take nothing.
func (r *Reconciler) createNode(ctx context.Context, pool store.Pool, node box.Host, row store.LifecycleBox) error {
	err := func() error {
		ssh, err := r.nodes.Create(ctx, pool.Name, node.Name, row.Size.Name)
		if err != nil {
			return err
		}
		joined, err := r.nodes.Join(ctx, node.Name, ssh)
		if err != nil {
			return err
		}
		joined.Pool, joined.Box, joined.DiskFloor, joined.CI = node.Pool, node.Box, box.DiskFloor(joined.DiskBytes), true
		if joined.Problem != "" {
			return fmt.Errorf("the node cannot take session boxes: %s", joined.Problem)
		}
		if placed := box.Admit(candidates(row, []box.Host{joined}), nil, row.Box, row.Only...); placed.Host == "" {
			return fmt.Errorf("the node is too small for a box of size %s: %s", row.Size.Name, placed.Reason)
		}
		return r.store.FinishPoolNode(ctx, joined)
	}()
	if ctx.Err() != nil {
		return ctx.Err() // The next engine undoes it.
	}
	if err != nil {
		problem := fmt.Sprintf("could not add node %s: %v", node.Name, err)
		if err := r.nodes.Destroy(ctx, pool.Name, node.Name); err != nil {
			problem += "; " + err.Error()
		}
		r.nodes.Leave(ctx, node.Name)
		if err := r.store.RemoveHost(ctx, node.Name); err != nil {
			return err
		}
		return r.poolFailed(ctx, pool.Name, row.Room, problem)
	}
	r.Kick()
	return nil
}

// recoverNode undoes a create that a restart cut short. Nothing says how far it got, so the machine, if there is one, goes, and
// any box still waiting gets a new node. A destroy that fails leaves an unknown machine for the audit to report.
func (r *Reconciler) recoverNode(ctx context.Context, pool store.Pool, name contract.HostName) error {
	nodes, err := r.store.PoolNodes(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(nodes, func(node box.Host) bool { return node.Name == name && node.State == box.HostProvisioning }) {
		return nil // Its create ended after this pass read it.
	}
	if err := r.nodes.Destroy(ctx, pool.Name, name); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("pool %s: undo node %s: %v", pool.Name, name, err)
	}
	if err := r.store.RemoveHost(ctx, name); err != nil {
		return err
	}
	r.Kick()
	return nil
}

// shrink carries each node to its next state. A node leaves only when no box holds it, which DrainHost checks again in one write
// with closing it to admission. A dormant session's stopped box holds its node. The idle clock lives in memory, so a restart
// only starts a shared node's wait again.
func (r *Reconciler) shrink(ctx context.Context, pool store.Pool, own []box.Host, rows []store.LifecycleBox) error {
	for _, node := range own {
		switch {
		case node.State == box.HostProvisioning:
			r.launchRoomStep(ctx, nodeStep(node.Name), func(ctx context.Context) error { return r.recoverNode(ctx, pool, node.Name) })
			continue
		case node.State == box.HostDraining:
			if pool.RetryAt.After(r.now()) {
				continue
			}
		case holdsNode(node, rows):
			delete(r.idle, node.Name)
			continue
		default:
			if !pool.PerSession {
				since, seen := r.idle[node.Name]
				if !seen {
					r.idle[node.Name] = r.now()
					continue
				}
				if r.now().Before(since.Add(pool.Idle)) {
					continue
				}
			}
			err := r.drainHost(ctx, node.Name)
			var held *store.HostHeldError
			if errors.As(err, &held) {
				continue
			}
			if err != nil {
				return err
			}
			delete(r.idle, node.Name)
		}
		r.launchRoomStep(ctx, nodeStep(node.Name), func(ctx context.Context) error { return r.retireNode(ctx, pool, node.Name) })
	}
	return nil
}

// holdsNode says whether a box is on this node, or waits to be placed on the node made for it.
func holdsNode(node box.Host, rows []store.LifecycleBox) bool {
	return slices.ContainsFunc(rows, func(row store.LifecycleBox) bool {
		if row.State == contract.BoxDeleted {
			return false
		}
		return row.Host == node.Name || row.ID == node.Box && row.Host == "" && node.State == box.HostReady && node.Problem != contract.HostProblemCreateFailed
	})
}

// retireNode gives a drained node back, and keeps its row until the machine has gone, so the pool never loses count of it.
func (r *Reconciler) retireNode(ctx context.Context, pool store.Pool, name contract.HostName) error {
	r.nodes.Leave(ctx, name)
	if err := r.nodes.Destroy(ctx, pool.Name, name); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		room, roomErr := r.store.PoolRoom(ctx, pool.Name)
		if roomErr != nil {
			return roomErr
		}
		return r.poolFailed(ctx, pool.Name, room, fmt.Sprintf("%snode %s failed: %v", destroyProblem, name, err))
	}
	if err := r.store.RemoveHost(ctx, name); err != nil {
		return err
	}
	if strings.HasPrefix(pool.Problem, destroyProblem) {
		return r.store.PoolRecovered(ctx, pool.Name)
	}
	return nil
}

// poolFailed backs the pool off. Only a spell's first failure posts the notice.
func (r *Reconciler) poolFailed(ctx context.Context, pool string, room contract.RoomID, problem string) error {
	if len(problem) > problemBytes {
		problem = strings.ToValidUTF8(problem[:problemBytes], "")
	}
	notice, err := r.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.NoticePosted{Text: fmt.Sprintf("Pool %s: %s. It tries again after a wait, and reports again only once it has recovered and failed anew.", pool, problem)})
	if err != nil {
		return err
	}
	if err := r.store.PoolFailed(ctx, pool, problem, poolDelay, notice); err != nil {
		return err
	}
	r.Kick() // The boxes that wait can say so now.
	return nil
}

// audit compares the script's list with the rows, on a slow timer.
func (r *Reconciler) audit(ctx context.Context, pool store.Pool, own []box.Host) {
	if last, seen := r.audited[pool.Name]; seen && r.now().Before(last.Add(poolAuditEvery)) {
		return
	}
	r.audited[pool.Name] = r.now()
	r.launchRoomStep(ctx, "pool "+pool.Name, func(ctx context.Context) error { return r.auditPool(ctx, pool, own) })
}

// auditPool reports a machine no row knows, once, and never destroys it: Dyna may simply have forgotten it, or it may be
// the owner's. A node the script no longer has is a host lost, and the heartbeat path takes it from there. A row that came or
// went during the list counts as known, since the list says nothing of it.
func (r *Reconciler) auditPool(ctx context.Context, pool store.Pool, before []box.Host) error {
	listed, err := r.nodes.List(ctx, pool.Name)
	if err != nil {
		return fmt.Errorf("pool %s: %w", pool.Name, err)
	}
	nodes, err := r.store.PoolNodes(ctx)
	if err != nil {
		return err
	}
	known := func(name string) bool {
		same := func(node box.Host) bool { return string(node.Name) == name }
		return slices.ContainsFunc(before, same) || slices.ContainsFunc(nodes, same)
	}
	unknown := slices.DeleteFunc(slices.Clone(listed), known)
	slices.Sort(unknown)
	for _, node := range before {
		if node.State == box.HostReady && !slices.Contains(listed, string(node.Name)) {
			if err := r.store.RecordHostHeartbeat(ctx, node.Name, false, nil, 0, r.now()); err != nil {
				return err
			}
			r.Kick()
		}
	}
	if slices.Equal(unknown, pool.Told) {
		return nil
	}
	var notice *contract.Event
	if fresh := pool.NewOrphans(unknown); len(fresh) > 0 {
		room, err := r.store.PoolRoom(ctx, pool.Name)
		if err != nil {
			return err
		}
		event, err := r.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.NoticePosted{Text: fmt.Sprintf("Pool %s lists machines Dyna has no record of: %s.", pool.Name, strings.Join(fresh, ", "))})
		if err != nil {
			return err
		}
		notice = &event
	}
	return r.store.PoolOrphans(ctx, pool.Name, unknown, notice)
}

// RemoveHost takes a host that holds nothing out of Dyna. A node goes back through its script, which the next pass runs.
// With discard, every box on the host fails first, as on a host lost for good (discardHost). It returns the open sessions
// whose boxes failed.
func (r *Reconciler) RemoveHost(ctx context.Context, name contract.HostName, discard bool) ([]contract.SessionID, error) {
	var failed []contract.SessionID
	var err error
	if discard {
		failed, err = r.discardHost(ctx, name)
	} else {
		r.pass.Lock() // A pass places boxes from its own reading of the hosts.
		err = r.drainHost(ctx, name)
		r.pass.Unlock()
	}
	if err != nil {
		return nil, err
	}
	nodes, err := r.store.PoolNodes(ctx)
	if err != nil {
		return failed, err
	}
	if slices.ContainsFunc(nodes, func(node box.Host) bool { return node.Name == name }) {
		r.Kick()
		return failed, nil
	}
	if r.nodes != nil {
		r.nodes.Leave(ctx, name)
	}
	return failed, r.store.RemoveHost(ctx, name)
}

// discardVolumeWait bounds the removal of one box's container and volume on a host that answers.
const discardVolumeWait = 2 * time.Minute

// discardHost fails every box on a host and closes the host to admission, then removes each box's container and volume while
// the host answers. On a host that does not, they go with the machine, and only the engine's side of each box goes now. The
// boxes then let go of the host, so session.restart places each anew. A closing box is discarded, so its session is not
// returned.
func (r *Reconciler) discardHost(ctx context.Context, name contract.HostName) ([]contract.SessionID, error) {
	r.pass.Lock() // A pass places boxes from its own reading of the hosts.
	unlock := r.store.LockAdmission()
	hosts, err := r.store.Hosts(ctx)
	reachable := slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == name && h.State == box.HostReady })
	var services []store.ServiceRow
	if err == nil && r.svc != nil {
		services, err = r.store.Services(ctx, "")
	}
	var boxes []store.LifecycleBox
	if err == nil {
		boxes, err = r.store.DiscardHostBoxes(ctx, name, func(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
			return r.event(room, contract.Principal{Kind: contract.PrincipalEngine}, body)
		})
	}
	unlock()
	r.pass.Unlock()
	if err != nil {
		return nil, err
	}
	if r.svc != nil {
		// Admission is closed before waiting, and neither pass nor admission is held while a worker finishes. Cleanup must be
		// last: a deploy, rewrite or direct restart already past its revision check may still write or start a unit after drain.
		work := r.serviceHostWork(name)
		work.Lock()
		if reachable {
			for _, row := range services {
				if row.Host == name {
					r.removeServiceFromHost(ctx, row)
				}
			}
		}
		work.Unlock()
	}
	failed := []contract.SessionID{}
	for _, b := range boxes {
		if b.Want != store.WantDiscard {
			failed = append(failed, b.Session)
		}
		removed := false
		if reachable {
			removeCtx, cancel := context.WithTimeout(ctx, discardVolumeWait)
			err := r.boxes.Remove(removeCtx, b.ID)
			cancel()
			if err != nil {
				log.Printf("host %s: discard box %s: %v", name, b.ID, err)
			}
			removed = err == nil
		}
		if !removed && r.roomHosts != nil {
			if err := r.roomHosts.ForgetBox(b.ID); err != nil {
				log.Printf("host %s: forget box %s: %v", name, b.ID, err)
			}
		}
	}
	if err := r.store.ReleaseHostBoxes(ctx, name); err != nil {
		return failed, err
	}
	r.Kick() // A closing box goes, and parents hear of the failures.
	return failed, nil
}
