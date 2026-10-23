// The machines registry: this engine's host and the machines that run CI or
// agents. Sources: internal/machines (the rows) and internal/websvc/machines.go
// (the views below). A figure the engine does not know yet is absent, so a
// reader can tell "none" from "not measured".

// MachineRole is machines.Role. "computers" is not a role: a computer runs
// on a machine that has the agents role.
export type MachineRole = 'engine' | 'agents' | 'ci'

// MachineStatus is what the engine knows about a machine right now. "unknown"
// is a machine the engine has not probed yet.
export type MachineStatus = 'online' | 'off' | 'unknown'

// MachineCISlots is the CI work this machine runs now, out of its slots.
export interface MachineCISlots {
  used: number
  slots: number
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
  // cpu is the machine's share of its cores in percent, known for this
  // engine's machine only.
  cpu?: number
  agents?: number
  computers?: number
  ci?: MachineCISlots
}

// MachineHarness is one agent harness installed on a machine and its version.
export interface MachineHarness {
  name: string
  version: string
}

// MachineDetail is the Machine page. ssh is the route to a remote machine, or
// "" for this engine's own machine. ciSlots is the machine's setting, while
// ci in the summary is its use now.
export interface MachineDetail extends MachineSummary {
  ssh: string
  ciSlots: number
  dynaVersion?: string
  harnesses?: MachineHarness[]
}

// MachinePatch is a change to one machine. An absent field keeps its value.
export interface MachinePatch {
  enabled?: boolean
  roles?: MachineRole[]
  ciSlots?: number
  ssh?: string
}
