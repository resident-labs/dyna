package ci

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// The node pools see runners as they see boxes: a runner on a node holds it, and a job that no host takes waits for a node
// of its room's pool. The reconciler reads this under shown, never mu, which is held across SSH.

// Runners returns the runners on hosts, which admission counts as boxes, and the jobs that wait for a node.
func (p *Pool) Runners() (placed []box.Box, waiting []store.LifecycleBox) {
	p.shown.Lock()
	defer p.shown.Unlock()
	keys := make([]scaleSet, 0, len(p.waiting))
	for key := range p.waiting {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b scaleSet) int {
		return cmp.Or(strings.Compare(string(a.repo), string(b.repo)), strings.Compare(string(a.size), string(b.size)))
	})
	for _, key := range keys {
		waiting = append(waiting, p.waiting[key]...)
	}
	return slices.Clone(p.placed), waiting
}

// Hold runs drain, which closes a host to admission, unless a runner is on that host. No runner starts meanwhile, so none
// lands on a host as it drains.
func (p *Pool) Hold(host contract.HostName, drain func() error) error {
	p.shown.Lock()
	defer p.shown.Unlock()
	var held []string
	for _, b := range p.placed {
		if b.Host == host {
			held = append(held, "CI runner "+string(b.ID))
		}
	}
	if len(held) > 0 {
		return &store.HostHeldError{Host: host, Held: held}
	}
	return drain()
}

// claim records a runner on its host before it starts, once the host is still open to runners. A host drains in Hold, so
// either the drain sees this runner or this sees the drain.
func (p *Pool) claim(ctx context.Context, runner box.Box, assigned contract.BoxID) error {
	unlock := p.Store.LockAdmission()
	defer unlock()
	p.shown.Lock()
	defer p.shown.Unlock()
	hosts, err := p.Store.Hosts(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(box.RunnerHosts(hosts), func(h box.Host) bool { return h.Name == runner.Host }) {
		return fmt.Errorf("host %s no longer takes runners", runner.Host)
	}
	// A survey precedes SSH and image preparation; a service update or box placement may have reserved more since it. Recheck
	// the current reservations before publishing this claim, under the same admission lock those writers hold.
	rows, err := p.Store.LifecycleBoxes(ctx)
	if err != nil {
		return err
	}
	boxes := slices.Clone(p.placed)
	for _, row := range rows {
		boxes = append(boxes, row.Box)
	}
	if p.Memory != nil {
		for i := range hosts {
			hosts[i].MemUsed = p.Memory(hosts[i].Name)
		}
	}
	hosts = slices.DeleteFunc(box.RunnerHosts(hosts), func(h box.Host) bool { return h.Name != runner.Host })
	request := runner
	if assigned != "" {
		// A per-job node is assigned to the waiting job's ID, not the random name of its eventual runner.
		request.ID = assigned
	}
	if placement := box.Admit(hosts, boxes, request); placement.Host == "" {
		return fmt.Errorf("host %s no longer fits the runner (%s)", runner.Host, placement.Reason)
	}
	p.placed = append(p.placed, runner)
	return nil
}

// wait records the jobs of one scale set that wait for a node, and tells the node pools when that changed.
func (p *Pool) wait(key scaleSet, rows []store.LifecycleBox) {
	p.shown.Lock()
	same := slices.EqualFunc(p.waiting[key], rows, func(a, b store.LifecycleBox) bool { return a.ID == b.ID })
	if len(rows) == 0 {
		delete(p.waiting, key)
	} else {
		if p.waiting == nil {
			p.waiting = make(map[scaleSet][]store.LifecycleBox)
		}
		p.waiting[key] = rows
	}
	p.shown.Unlock()
	if !same {
		log.Printf("ci pool: repository %s: %s: %d jobs wait for a node", key.repo, SetName(key.size), len(rows))
	}
	if !same && p.Kick != nil {
		p.Kick()
	}
}
