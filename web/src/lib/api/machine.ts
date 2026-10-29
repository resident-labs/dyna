// The machines registry: this engine's host and the machines that run CI or
// agents. Sources: internal/machines (the rows) and internal/websvc/machines.go
// (the views below). A figure the engine does not know yet is absent, so a
// reader can tell "none" from "not measured".

// MachineRole is machines.Role. "computers" is not a role: a computer runs
// on a machine that has the agents role.
export type MachineRole = 'engine' | 'agents' | 'ci'

// MachineStatus is what the engine knows about a machine right now.
// "disabled" means switched off in the registry, "offline" means the probe
// could not reach it, "degraded" means it answered but needs attention, such
// as low free disk space, and "unknown" means no probe has run yet.
export type MachineStatus = 'online' | 'degraded' | 'disabled' | 'offline' | 'unknown'

// MachineStatusReason names why a machine is degraded. The page words it, so a
// probe message never reaches the screen.
export type MachineStatusReason = 'low-disk'

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
  roles: MachineRole[]
  // local marks the machine that runs this engine.
  local: boolean
  enabled: boolean
  status: MachineStatus
  // ciSlots is the machine's legacy slot setting; ci is the work its node
  // queue holds now.
  ciSlots: number
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
  // statusReason names why a degraded machine needs attention.
  statusReason?: MachineStatusReason
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
  // when the probe could not read it. diskFloor is the free space Dyna keeps
  // for CI work on the machine; below it, no CI starts there.
  freeDisk?: number
  diskFloor?: number
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
  roles?: MachineRole[]
  ciSlots?: number
  allowHostChecks?: boolean
  rooms?: string[]
  suites?: string[]
  ssh?: string
  // sshConnectTimeoutSeconds sets the ssh connect wait; 0 restores the default.
  sshConnectTimeoutSeconds?: number
  // create adds the machine that the reference names when none exists.
  create?: boolean
}
