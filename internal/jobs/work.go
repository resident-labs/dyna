package jobs

import (
	"context"
	"slices"
	"strings"
	"time"

	"dyna-agent/internal/machines"
)

// CheckKind says what a local CI run tests. The values match the machine
// view's run kinds, so one wording names a run and each of its checks.
type CheckKind string

// Check kinds.
const (
	// CheckKindPullRequest tests a pull request head.
	CheckKindPullRequest CheckKind = "pull-request"
	// CheckKindLocalChange tests a change on a local branch, which has no
	// pull request number.
	CheckKindLocalChange CheckKind = "local-change"
	// CheckKindSync tests the integration branch after a merge.
	CheckKindSync CheckKind = "sync"
)

// CheckSource says what one local CI check's run tests, so a queue view words
// the check instead of reading its admission ID.
type CheckSource struct {
	// Kind is the work the run tests.
	Kind CheckKind `json:"kind"`
	// Number is the pull request the run checks; zero when it checks none.
	Number int `json:"number"`
	// Trial marks a merge trial, which checks a merge of several heads.
	Trial bool `json:"trial"`
	// Branch names the branch of a local change, which has no number.
	Branch string `json:"branch,omitempty"`
	// Head is the short commit the run checks.
	Head string `json:"head,omitempty"`
}

// WorkKind says what holds or awaits a place on a node.
type WorkKind string

// Work kinds.
const (
	// WorkJob is a command submitted with dyna job run.
	WorkJob WorkKind = "job"
	// WorkCheck is one local CI check that holds an admission row.
	WorkCheck WorkKind = "check"
)

// WorkItem is one unit of work on a node: running, or waiting for room.
type WorkItem struct {
	Kind WorkKind `json:"kind"`
	ID   string   `json:"id"`
	// Label is a job's command or a check's name. A check whose row kept no
	// name holds its ID, so a row that predates names still reads.
	Label string `json:"label"`
	// Source says what a check's run tests, with the short commit it checks.
	// Nil for a job, and for a check row that carries no source.
	Source *CheckSource `json:"source,omitempty"`
	// Room is the room ID. Session is the submitting session of a job; a check
	// has none.
	Room    string `json:"room,omitempty"`
	Session string `json:"session,omitempty"`
	// CPUs and MemoryMiB are what the node holds or will hold for the unit,
	// after the node clamped the request.
	CPUs      int                     `json:"cpus"`
	MemoryMiB int64                   `json:"memoryMiB"`
	Heavy     bool                    `json:"heavy"`
	Resources []machines.ResourceName `json:"resources,omitempty"`
	// Since is when a running unit started, or when a check entered the queue.
	Since time.Time `json:"since"`
	// Position and Reason describe a waiting unit: its place in admission
	// order and the queue's own words for why it waits.
	Position int    `json:"position,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ResourceUse is one named resource of a node and the units running work holds.
type ResourceUse struct {
	Name  machines.ResourceName `json:"name"`
	Used  int                   `json:"used"`
	Limit int                   `json:"limit"`
}

// NodeCapacity is what a node's admission counts against: the units running
// work holds, and the limits that admit more.
type NodeCapacity struct {
	// CPUs and MemoryMiB are the probed totals; zero means the node was not
	// probed.
	CPUs      int   `json:"cpus"`
	MemoryMiB int64 `json:"memoryMiB"`
	// CPUsUsed and MemoryMiBUsed are held by running jobs and checks.
	CPUsUsed      int   `json:"cpusUsed"`
	MemoryMiBUsed int64 `json:"memoryMiBUsed"`
	// DiskFree and DiskTotal are bytes of the disk that holds Dyna's work, and
	// zero when no probe read it. DiskKeeps words the free space the node's
	// disk rule keeps while it admits work.
	DiskFree  uint64 `json:"diskFree"`
	DiskTotal uint64 `json:"diskTotal"`
	DiskKeeps string `json:"diskKeeps,omitempty"`
	// DiskBlock is the queue's words for a disk that admits no work, and empty
	// when it admits work.
	DiskBlock    string        `json:"diskBlock,omitempty"`
	HeavyRunning int           `json:"heavyRunning"`
	HeavyLimit   int           `json:"heavyLimit"`
	Resources    []ResourceUse `json:"resources,omitempty"`
}

// NodeWork is one node's work, read at once from the admission rows and the
// jobs table: its capacity, what runs, and what waits in admission order.
type NodeWork struct {
	Capacity NodeCapacity `json:"capacity"`
	Running  []WorkItem   `json:"running"`
	Waiting  []WorkItem   `json:"waiting"`
}

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
	return WorkItem{Kind: WorkJob, ID: job.ID, Label: strings.Join(job.Command, " "), Room: job.RoomID, Session: job.Session, CPUs: demand.CPUs, MemoryMiB: demand.MemoryMiB, Heavy: job.Heavy, Resources: slices.DeleteFunc(slices.Clone(job.Resources), isWorkspaceResource), Since: job.Created}
}
