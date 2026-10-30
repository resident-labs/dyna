package websvc

import (
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
	"time"
)

// MachineStatus is what this engine knows about a machine now.
type MachineStatus string

const (
	// MachineOnline is this engine's own machine, or a machine the probe
	// reached: the engine knows it is up.
	MachineOnline MachineStatus = "online"
	// MachineDisabled is a machine switched off in the registry. It takes no
	// new work, but it is not known to be down.
	MachineDisabled MachineStatus = "disabled"
	// MachineOffline is a machine the probe could not reach.
	MachineOffline MachineStatus = "offline"
	// MachineUnknown is a machine the engine does not probe yet.
	MachineUnknown MachineStatus = "unknown"
)

// MachineCIWork is the CI work a machine runs now and the work that waits
// for room on it, checks and jobs both, from the node's shared queue.
type MachineCIWork struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
}

// MachineCIRun is one local CI run in flight on a machine. Title and URL name
// the PR it checks, so the page shows the PR and not only a number.
// MachineCIRunKind is the work a machine run checks. It is watch.RunningCIKind
// as the page contract states it, so a client words a run that has no pull
// request number.
type MachineCIRunKind string

// Machine run kinds.
const (
	MachineCIRunPullRequest MachineCIRunKind = "pull-request"
	MachineCIRunLocalChange MachineCIRunKind = "local-change"
	MachineCIRunSync        MachineCIRunKind = "sync"
)

type MachineCIRun struct {
	Machine string `json:"machine"`
	Number  int    `json:"number"`
	// Kind is the work the run checks, which a client words.
	Kind MachineCIRunKind `json:"kind"`
	// Branch names a local change's branch, which has no pull request number.
	Branch  string    `json:"branch,omitempty"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Trial   bool      `json:"trial"`
	Suites  []string  `json:"suites,omitempty"`
	Started time.Time `json:"started"`
}

// MachineAgent is a session that runs on this engine's machine now.
type MachineAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Room    string `json:"room,omitempty"`
	RoomID  string `json:"roomId,omitempty"`
	Hue     int    `json:"hue"`
}

// MachineSummary is one row of the Machines list. A figure the engine does not
// measure stays nil, so the client shows no count instead of a zero.
type MachineSummary struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Local           bool          `json:"local"`
	Enabled         bool          `json:"enabled"`
	Status          MachineStatus `json:"status"`
	AllowHostChecks bool          `json:"allowHostChecks"`
	// MaxIOHeavy and MaxDiskPercent are the node queue's stored limits; zero
	// keeps each default.
	MaxIOHeavy     *int                          `json:"maxIOHeavy,omitempty"`
	QuickSlots     *int                          `json:"quickSlots,omitempty"`
	MaxDiskPercent *int                          `json:"maxDiskPercent,omitempty"`
	Resources      map[machines.ResourceName]int `json:"resources,omitempty"`
	Rooms          []string                      `json:"rooms,omitempty"`
	// Labels are the owner's placement labels; the ci label runs local CI.
	Labels []string `json:"labels,omitempty"`
	// ProbedLabels come from the last good probe and are read-only.
	ProbedLabels  []string       `json:"probedLabels,omitempty"`
	CPU           *int           `json:"cpu,omitempty"`
	MemoryPercent *int           `json:"memoryPercent,omitempty"`
	Agents        *int           `json:"agents,omitempty"`
	Computers     *int           `json:"computers,omitempty"`
	CI            *MachineCIWork `json:"ci,omitempty"`
}

// MachineWorkItem is one unit of work on a machine, with the names the page
// shows for its room and session. A name stays empty when the engine cannot
// resolve it, and the page then leaves it out.
type MachineWorkItem struct {
	jobs.WorkItem `tstype:",extends"`
	RoomName      string `json:"roomName,omitempty"`
	SessionName   string `json:"sessionName,omitempty"`
}

// MachineWork is the Work section of the Machine page: what the machine's
// queue counts against, the work it runs and the work that waits, in
// admission order. It comes from one read of the admission rows and the jobs
// table.
type MachineWork struct {
	Capacity jobs.NodeCapacity `json:"capacity"`
	Running  []MachineWorkItem `json:"running" tstype:"MachineWorkItem[] | null"`
	Waiting  []MachineWorkItem `json:"waiting" tstype:"MachineWorkItem[] | null"`
}

// MachineHarness is one agent harness installed on a machine and its version.
type MachineHarness struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// MachineDetail is the Machine page: the summary, how the engine reaches the
// machine, the versions this machine runs, the sessions that run on it, and
// what the last probe read.
type MachineDetail struct {
	MachineSummary `tstype:",extends"`
	SSH            string `json:"ssh"`
	DynaVersion    string `json:"dynaVersion,omitempty"`
	// SSHConnectTimeoutSeconds is how long an ssh to the machine waits for the
	// connection: the stored value, or the default when none is stored.
	SSHConnectTimeoutSeconds int `json:"sshConnectTimeoutSeconds,omitempty"`
	// HostChecksAllowedBy words who allows host checks, when someone does:
	// "the owner", or "root of room <name>".
	HostChecksAllowedBy []string `json:"hostChecksAllowedBy,omitempty"`

	Harnesses []MachineHarness `json:"harnesses,omitempty"`
	Running   []MachineAgent   `json:"running,omitempty"`
	// CIRuns lists the CI runs in flight on this machine, so the page shows
	// what it checks now. The count of them is ci.used.
	CIRuns []MachineCIRun `json:"ciRuns,omitempty"`

	// ProbedAt is when the engine last read the machine, and SeenAt when a
	// probe last reached it. Both stay absent before the first probe, and
	// Capabilities and Load are what that good probe read. A failed probe
	// keeps them, so the page shows what the machine has while it is offline.
	ProbedAt     *time.Time           `json:"probedAt,omitempty"`
	SeenAt       *time.Time           `json:"seenAt,omitempty"`
	Capabilities *MachineCapabilities `json:"capabilities,omitempty"`
	Load         *MachineLoad         `json:"load,omitempty"`
}

// MachineCapabilities is what the last probe read about a machine. Memory
// values are bytes.
type MachineCapabilities struct {
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	CPUs         int      `json:"cpus"`
	MemTotal     uint64   `json:"memTotal"`
	MemAvailable uint64   `json:"memAvailable"`
	GPU          bool     `json:"gpu"`
	Containers   []string `json:"containers,omitempty"`
	// Computers is whether the machine can host an agent computer, which needs
	// labwc and Xwayland. Stream is whether wayvnc can show its desktop in the
	// app.
	Computers bool `json:"computers"`
	Stream    bool `json:"stream"`
	// FreeDisk is the free space of the filesystem that holds the machine's
	// /var/tmp, where Dyna keeps its work, in bytes. DiskKeeps words the free
	// space the node's disk rule keeps while it admits work, and DiskBlock is
	// the queue's words for why it admits none. All three stay absent when the
	// probe could not read the disk.
	FreeDisk  *int64  `json:"freeDisk,omitempty"`
	DiskFloor *uint64 `json:"diskFloor,omitempty"`
	DiskKeeps string  `json:"diskKeeps,omitempty"`
	DiskBlock string  `json:"diskBlock,omitempty"`
}

// MachineLoad is the last load reading of a machine. CPUPercent stays absent
// when the probe could not take two readings.
type MachineLoad struct {
	CPUPercent   *int       `json:"cpuPercent,omitempty"`
	LoadAvg      [3]float64 `json:"loadAvg" tstype:"MachineLoadAvg"`
	MemTotal     uint64     `json:"memTotal"`
	MemAvailable uint64     `json:"memAvailable"`
}

// MachinePatch is one change to a machine. A nil field keeps its value; the
// store checks the result before it writes. Create adds the machine that the
// reference names when the registry has none.
type MachinePatch struct {
	Name            *string   `json:"name,omitempty"`
	Enabled         *bool     `json:"enabled"`
	AllowHostChecks *bool     `json:"allowHostChecks"`
	Rooms           *[]string `json:"rooms"`
	Labels          *[]string `json:"labels"`
	SSH             *string   `json:"ssh"`
	Create          bool      `json:"create,omitempty"`
	// Remove deletes the machine that the reference names.
	Remove bool `json:"remove,omitempty"`
	// ContainerSocket is the container runtime socket a workflow job uses on
	// this machine, as a DOCKER_HOST value; an empty value means Docker's.
	ContainerSocket *string `json:"containerSocket"`
	// SSHConnectTimeoutSeconds is how long an ssh to this machine waits for the
	// connection; zero restores the default.
	SSHConnectTimeoutSeconds *int `json:"sshConnectTimeoutSeconds,omitempty"`
	// CheckTmp is the folder that holds each host check's temporary folder on
	// this machine; an empty value means the node's temporary directory.
	CheckTmp *string `json:"checkTmp,omitempty"`
	// MaxIOHeavy caps how many IO-heavy jobs and checks this node runs at
	// once; zero keeps the default of one per sixteen probed CPUs.
	MaxIOHeavy *int `json:"maxIOHeavy,omitempty"`
	QuickSlots *int `json:"quickSlots,omitempty"`
	// MaxDiskPercent is the used-disk percent above which this node admits no
	// new work; zero keeps the default of 90.
	MaxDiskPercent *int `json:"maxDiskPercent,omitempty"`
	// Resources replaces the node's declared named resource counts.
	Resources *map[machines.ResourceName]int `json:"resources,omitempty"`
	// Mounts and Devices upsert room-scoped container registrations. Other
	// registrations stay unchanged, so one room's edit cannot replace another's.
	Mounts  *[]machines.NamedMount  `json:"mounts,omitempty"`
	Devices *[]machines.DeviceGrant `json:"devices,omitempty"`
	// RemoveMounts and RemoveDevices revoke only the matching room's access.
	RemoveMounts  *[]machines.MountRef  `json:"removeMounts,omitempty"`
	RemoveDevices *[]machines.DeviceRef `json:"removeDevices,omitempty"`
}
