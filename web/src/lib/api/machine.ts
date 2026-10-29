// The machines registry: this engine's host and every other machine Dyna
// reaches over SSH. Sources: internal/machines (the rows) and
// internal/websvc/machines.go (the views below). A figure the engine does not
// know yet is absent, so a reader can tell "none" from "not measured".

// MachineStatus is what the engine knows about a machine right now.
// "disabled" means switched off in the registry, "offline" means the probe
// could not reach it, and "unknown" means no probe has run yet.
export type MachineStatus = 'online' | 'disabled' | 'offline' | 'unknown'

// MachineCIWork is the CI work this machine's node queue holds now: the runs
// it admitted and the units still waiting for room.
export interface MachineCIWork {
  running: number
  waiting: number
}

// MachineCIRunKind is watch.RunningCIKind: the work a run checks. A local branch
// change and an integration sync have no pull request number, so the row words
// the kind instead of a number.
export type MachineCIRunKind = 'pull-request' | 'local-change' | 'sync'

// MachineCIRun is one local CI run in flight on a machine. kind says what it
// checks; number, title and url name a pull request, and branch names a local
// change, so a row never shows a number alone.
export interface MachineCIRun {
  machine: string
  number: number
  kind: MachineCIRunKind
  branch?: string
  title: string
  url: string
  trial: boolean
  suites?: string[]
  // started is when the run took its slots, as the engine's clock reads it.
  started: string
}

// MachineSummary is one row of the Machines list.
export interface MachineSummary {
  id: string
  name: string
  // local marks the machine that runs this engine.
  local: boolean
  enabled: boolean
  status: MachineStatus
  // ci is the work the machine's node queue holds now.
  // maxIOHeavy and maxDiskPercent are the node queue's stored limits; zero or
  // absent keeps each default.
  maxIOHeavy?: number
  maxDiskPercent?: number
  // allowHostChecks is an explicit opt-in by the owner, or by the root of a
  // room for that room, to repository commands outside containers.
  allowHostChecks: boolean
  // rooms are the IDs of the rooms whose local CI this machine takes; absent
  // means every room. suites limits the CI suites the same way.
  rooms?: string[]
  suites?: string[]
  // labels mark what the machine takes; the ci label says it runs local CI
  // checks. An unlabeled machine still takes jobs when enabled.
  labels?: string[]
  // cpu is the machine's share of its cores in percent: live for this engine's
  // machine, and the last probe reading for a remote one.
  cpu?: number
  agents?: number
  computers?: number
  ci?: MachineCIWork
}

// MachineAgent is a session that runs on this engine's machine now.
export interface MachineAgent {
  id: string
  name: string
  hue: number
  address?: string
  room?: string
  roomId?: string
}

// MachineHarness is one agent harness installed on a machine and its version.
export interface MachineHarness {
  name: string
  version: string
}

// MachineDetail is the Machine page. ssh is the route to a remote machine, or
// "" for this engine's own machine.
export interface MachineDetail extends MachineSummary {
  ssh: string
  // sshConnectTimeoutSeconds is how long an ssh to the machine waits for the
  // connection. The engine reports the default when none is set.
  sshConnectTimeoutSeconds?: number
  dynaVersion?: string
  // hostChecksAllowedBy names who allows host checks, when someone does: the
  // owner, or the root of a room. The engine words it.
  hostChecksAllowedBy?: string[]
  harnesses?: MachineHarness[]
  running?: MachineAgent[]
  // ciRuns lists the CI runs in flight on this machine, so the page shows what
  // it checks now. Their slots add up to ci.used.
  ciRuns?: MachineCIRun[]
  // probedAt is when the engine last read this machine, and seenAt when a probe
  // last reached it. Both are absent before the first probe. A failed probe
  // keeps the capabilities and load of the last good one.
  probedAt?: string
  seenAt?: string
  capabilities?: MachineCapabilities
  load?: MachineLoad
}

// MachineCapabilities is what the last probe read about a machine. Memory
// values are bytes.
export interface MachineCapabilities {
  os: string
  arch: string
  cpus: number
  memTotal: number
  memAvailable: number
  gpu: boolean
  // containers names the container runtimes the machine has, in the order
  // Dyna prefers them. Absent means none.
  containers?: string[]
  // computers is whether a computer can run on the machine, which needs labwc
  // and Xwayland. stream is whether wayvnc can show its desktop in the app.
  computers: boolean
  stream: boolean
  // freeDisk is the free space on the machine's disk in bytes. It is absent
  // when the probe could not read it. diskKeeps words the free space the
  // node's disk rule keeps while it admits work, and diskBlock is the queue's
  // words for why it admits none.
  freeDisk?: number
  diskKeeps?: string
  diskBlock?: string
}

// MachineLoad is the last load reading of a machine. cpuPercent is absent when
// the probe could not take two readings.
export interface MachineLoad {
  cpuPercent?: number
  loadAvg: [number, number, number]
  memTotal: number
  memAvailable: number
}

// MachinePatch is a change to one machine. An absent field keeps its value.
export interface MachinePatch {
  name?: string
  enabled?: boolean
  allowHostChecks?: boolean
  rooms?: string[]
  suites?: string[]
  labels?: string[]
  ssh?: string
  // sshConnectTimeoutSeconds sets the ssh connect wait; 0 restores the default.
  sshConnectTimeoutSeconds?: number
  // create adds the machine that the reference names when none exists.
  create?: boolean
}

// MachineWorkKind says what a unit of work is: a command submitted with
// dyna job run, or one local CI check.
export type MachineWorkKind = 'job' | 'check'

// MachineWorkItem is one unit of work on a machine, running or waiting.
// Source: WorkItem in internal/jobs/work.go and MachineWorkItem in
// internal/websvc/machines.go.
export interface MachineWorkItem {
  kind: MachineWorkKind
  id: string
  // label is a job's command or a check's ID.
  label: string
  room?: string
  roomName?: string
  session?: string
  sessionName?: string
  // cpus and memoryMiB are what the machine holds or will hold for the unit.
  cpus: number
  memoryMiB: number
  heavy: boolean
  resources?: string[]
  // since is when a running unit started, or when a check entered the queue.
  since: string
  // position and reason describe a waiting unit: its place in admission order
  // and the queue's own words for why it waits.
  position?: number
  reason?: string
}

// MachineResourceUse is one named resource of a machine and the units running
// work holds.
export interface MachineResourceUse {
  name: string
  used: number
  limit: number
}

// MachineCapacity is what a machine's queue counts against. Totals of zero
// mean no probe read them yet.
export interface MachineCapacity {
  cpus: number
  memoryMiB: number
  cpusUsed: number
  memoryMiBUsed: number
  // diskFree and diskTotal are bytes; diskKeeps words the free space the
  // node's disk rule keeps while it admits work.
  diskFree: number
  diskTotal: number
  diskKeeps?: string
  // diskBlock is the queue's words for a disk that admits no work.
  diskBlock?: string
  heavyRunning: number
  heavyLimit: number
  resources?: MachineResourceUse[]
}

// MachineWork is the Work section of the Machine page. waiting keeps
// admission order.
export interface MachineWork {
  capacity: MachineCapacity
  running: MachineWorkItem[]
  waiting: MachineWorkItem[]
}
