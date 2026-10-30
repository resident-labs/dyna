package jobs

import (
	"dyna-agent/internal/machines"
	"time"
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
	Running  []WorkItem   `json:"running" tstype:"WorkItem[] | null"`
	Waiting  []WorkItem   `json:"waiting" tstype:"WorkItem[] | null"`
}
