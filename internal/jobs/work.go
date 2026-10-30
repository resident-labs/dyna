package jobs

import (
	"context"
	"slices"
	"strings"

	"dyna-agent/internal/machines"
)

// Work reads what node m runs now and what waits for it. Both lists keep the
// order admission uses: running work oldest first, waiting work with rooms
// taking turns.
func (a Admission) Work(ctx context.Context, m machines.Machine) (NodeWork, error) {
	if err := a.sweep(ctx); err != nil {
		return NodeWork{}, err
	}
	q, err := a.readQueue(ctx, m.Name)
	if err != nil {
		return NodeWork{}, err
	}
	held := q.hold(m)
	caps := m.Capabilities
	work := NodeWork{Running: []WorkItem{}, Waiting: []WorkItem{}, Capacity: NodeCapacity{
		CPUs:          caps.CPUs,
		MemoryMiB:     int64(caps.MemTotal >> 20),
		CPUsUsed:      held.cpus,
		MemoryMiBUsed: held.memMiB,
		DiskFree:      caps.Disk.Free,
		DiskTotal:     caps.Disk.Total,
		DiskKeeps:     m.DiskKeeps(),
		DiskBlock:     m.DiskBlock(),
		HeavyRunning:  held.heavy,
		HeavyLimit:    m.IOHeavyLimit(),
	}}
	reasons := map[string]string{}
	units := map[string]WorkItem{}
	checkItem := func(row admissionRow) WorkItem {
		label := row.Check
		if label == "" {
			label = row.ID
		}
		item := WorkItem{Kind: WorkCheck, ID: row.ID, Label: label, Room: row.Room, CPUs: row.CPUs, MemoryMiB: row.MemoryMiB, Heavy: row.Heavy, Since: row.Created}
		if row.Source.Kind != "" {
			item.Source = &row.Source
		}
		return item
	}
	for _, row := range q.heldRows {
		work.Running = append(work.Running, checkItem(row))
	}
	for _, row := range q.waitingRows {
		reasons[row.ID] = row.Reason
		units[row.ID] = checkItem(row)
	}
	for _, job := range q.running {
		item := jobWorkItem(job, m)
		item.Since = job.Started
		work.Running = append(work.Running, item)
	}
	for _, job := range q.queued {
		item := jobWorkItem(job, m)
		reasons[job.ID] = job.WaitReason
		units[job.ID] = item
	}
	slices.SortStableFunc(work.Running, func(x, y WorkItem) int { return x.Since.Compare(y.Since) })
	for index, unit := range orderWaiters(q.waiters(m)) {
		item := units[unit.id]
		item.Position, item.Reason = index+1, reasons[unit.id]
		work.Waiting = append(work.Waiting, item)
	}
	// Declared resources show even when idle; an undeclared name is a one-unit
	// lock that shows while a job holds it. A workspace's internal name never
	// shows.
	used := map[machines.ResourceName]int{}
	for name, holders := range held.resources {
		if !isWorkspaceResource(name) {
			used[name] = len(holders)
		}
	}
	for name, limit := range m.Resources {
		work.Capacity.Resources = append(work.Capacity.Resources, ResourceUse{Name: name, Used: used[name], Limit: limit})
		delete(used, name)
	}
	for name, n := range used {
		work.Capacity.Resources = append(work.Capacity.Resources, ResourceUse{Name: name, Used: n, Limit: 1})
	}
	slices.SortFunc(work.Capacity.Resources, func(x, y ResourceUse) int { return strings.Compare(string(x.Name), string(y.Name)) })
	return work, nil
}

// jobWorkItem words one job for a node's work lists. A workspace's internal
// lock is not a resource the owner declared, so it never lists.
func jobWorkItem(job Job, m machines.Machine) WorkItem {
	demand := jobDemand(job, m)
	if job.Lane == LaneQuick {
		demand.CPUs, demand.MemoryMiB = 0, 0
	}
	return WorkItem{Kind: WorkJob, Lane: job.Lane, ID: job.ID, Label: strings.Join(job.Command, " "), Room: job.RoomID, Session: job.Session, CPUs: demand.CPUs, MemoryMiB: demand.MemoryMiB, Heavy: job.Heavy, Resources: slices.DeleteFunc(slices.Clone(job.Resources), isWorkspaceResource), Since: job.Created}
}
