import type { MachineCIRun, MachineCapabilities, MachineCapacity, MachineLoad, MachineStatus, MachineSummary, MachineWorkItem } from '../api/machine.ts'

// statusText words a machine's status for a title or a row.
export function statusText(status: MachineStatus): string {
  switch (status) {
    case 'online':
      return 'Online'
    case 'disabled':
      return 'Disabled'
    case 'offline':
      return 'Offline'
    default:
      return 'Not probed'
  }
}

// count is one named figure: "3 agents", "1 computer".
export function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? '' : 's'}`
}

// summaryParts lists the counts a machine row shows. A machine reports only
// what it has: a remote machine reports no live figure until the probe reads
// it. It shows its queue when the engine runs one: "3 running", then
// "2 waiting" when work waits.
export function summaryParts(machine: MachineSummary): string[] {
  const parts: string[] = []
  if (machine.agents !== undefined) parts.push(count(machine.agents, 'agent'))
  // A machine takes local CI only when enabled and carrying the ci label, so
  // only that row reports a queue figure.
  if (!machine.enabled) parts.push('CI off')
  else if (machine.labels?.includes('ci')) {
    if (machine.ci) {
      parts.push(`${machine.ci.running} running`)
      if (machine.ci.waiting > 0) parts.push(`${machine.ci.waiting} waiting`)
    }
  } else parts.push('no CI label')
  if (machine.computers !== undefined) parts.push(count(machine.computers, 'computer'))
  return parts
}

// summaryText is the row's one line of counts.
export function summaryText(machine: MachineSummary): string {
  return summaryParts(machine).join(' · ')
}

// ciRunText names one CI run the way its row shows it. A pull request shows its
// number and title; a local branch change and an integration sync have no number,
// so they show their own name.
export function ciRunText(run: Pick<MachineCIRun, 'kind' | 'number' | 'title' | 'branch'>): string {
  switch (run.kind) {
    case 'local-change':
      return run.title || run.branch || 'Local branch change'
    case 'sync':
      return 'Integration sync'
  }
  return run.title ? `#${run.number} ${run.title}` : `#${run.number}`
}

// ciRunDetail is the run's second line: the suites it checks, when it started
// them, and whether it is a merge trial.
export function ciRunDetail(run: Pick<MachineCIRun, 'suites'>): string {
  return run.suites?.length ? run.suites.join(' · ') : ''
}

// statusCounts counts machines by status, in the order the subline shows them.
export function statusCounts(machines: readonly MachineSummary[]): { status: MachineStatus; n: number }[] {
  const of = (status: MachineStatus) => machines.filter((m) => m.status === status).length
  return [
    { status: 'online', n: of('online') },
    { status: 'disabled', n: of('disabled') },
    { status: 'offline', n: of('offline') },
    { status: 'unknown', n: of('unknown') },
  ]
}

// machinesSub is the Machines header's subline: "2 online · 1 disabled". It
// leaves out a status with no machine, and says so when there is none at all.
export function machinesSub(machines: readonly MachineSummary[] | null): string {
  if (!machines) return ''
  if (!machines.length) return 'No machines yet'
  const words: Record<MachineStatus, string> = { online: 'online', disabled: 'disabled', offline: 'offline', unknown: 'not probed' }
  return statusCounts(machines)
    .filter((c) => c.n > 0)
    .map((c) => `${c.n} ${words[c.status]}`)
    .join(' · ')
}

// loadPct is a machine's CPU use for its load bar. A machine without a figure
// has no bar, not one at zero.
export function loadPct(machine: MachineSummary): number | null {
  return machine.cpu === undefined ? null : Math.max(0, Math.min(100, machine.cpu))
}

// routeText names how the engine reaches a machine.
export function routeText(machine: Pick<MachineSummary, 'local'> & { ssh: string }): string {
  if (machine.local) return 'This machine'
  return machine.ssh || 'No route yet'
}

const GIB = 2 ** 30

// SIZES are the units sizeText steps up to once a value reaches them.
const SIZES = [
  { unit: 2 ** 40, name: 'TiB' },
  { unit: 2 ** 50, name: 'PiB' },
]

// sizeText words a byte count the way the load card does: "30.5 GiB", and the
// next unit once the value reaches it, as "2.0 TiB". A count below one GiB
// stays in GiB, because every figure the page shows is at least that large.
export function sizeText(bytes: number): string {
  let unit = GIB
  let name = 'GiB'
  for (const size of SIZES) {
    if (bytes >= size.unit) {
      unit = size.unit
      name = size.name
    }
  }
  return `${(bytes / unit).toFixed(1)} ${name}`
}

// versionText words a Dyna version for a dense row. The row already says Dyna,
// so the "dyna " prefix goes, and a Go pseudo-version shows the tag it builds
// on: "v0.4.3 (dev)". The caller keeps the whole string in the row's title.
export function versionText(version: string): string {
  const bare = version.replace(/^dyna\s+/i, '')
  const pseudo = bare.match(/^(v?\d+\.\d+\.\d+)?-0\.\d{14}-[0-9a-f]+$/)
  if (pseudo) return pseudo[1] ? `${pseudo[1]} (dev)` : 'dev'
  return bare
}

// systemText is the machine's own system in one line: "Linux · 16 CPUs · 30.5
// GiB". It leaves out what the probe could not read.
export function systemText(caps: MachineCapabilities): string {
  const parts: string[] = []
  const system = [caps.os, caps.arch].filter(Boolean).join(' · ')
  if (system) parts.push(system)
  if (caps.cpus > 0) parts.push(count(caps.cpus, 'CPU'))
  if (caps.memTotal > 0) parts.push(sizeText(caps.memTotal))
  return parts.join(' · ')
}

// runtimeText names the container runtimes a machine has.
export function runtimeText(containers: readonly string[] | undefined): string {
  if (!containers?.length) return 'None'
  return containers.map((c) => (c === 'docker' ? 'Docker' : c === 'podman' ? 'Podman' : c)).join(' and ')
}

// computersText words the computer capability. wayvnc only shows a desktop in
// the app, so a machine without it still runs computers.
export function computersText(caps: Pick<MachineCapabilities, 'computers' | 'stream'>): string {
  if (!caps.computers) return 'No'
  return caps.stream ? 'Yes, and shows its desktop' : 'Yes'
}

// memoryPct is a machine's used memory for its bar, or null when the probe
// read no total.
export function memoryPct(load: Pick<MachineLoad, 'memTotal' | 'memAvailable'>): number | null {
  if (!load.memTotal) return null
  const used = load.memTotal - load.memAvailable
  return Math.max(0, Math.min(100, (used / load.memTotal) * 100))
}

// loadText is a machine's free memory, next to its bar.
export function loadText(load: Pick<MachineLoad, 'memAvailable'>): string {
  if (!load.memAvailable) return ''
  return `${sizeText(load.memAvailable)} free`
}

// diskText words a machine's free disk space and the node's one disk rule:
// what it keeps while it admits work, or the queue's words when it admits
// none. It is empty when the probe read no free space.
export function diskText(caps: Pick<MachineCapabilities, 'freeDisk' | 'diskKeeps' | 'diskBlock'>): string {
  if (caps.freeDisk === undefined) return ''
  const free = `${sizeText(caps.freeDisk)} free`
  const rule = caps.diskBlock || (caps.diskKeeps ? `keeps ${caps.diskKeeps}` : '')
  return rule ? `${free} · ${rule}` : free
}

// ScopeEdit is one change to a CI machine's room or suite list.
export type ScopeEdit = { add: string } | { remove: string }

// editScope applies a change to a room or suite list and returns the new list,
// or null when nothing changes. A value is trimmed, kept once and the list is
// sorted, as dyna machine edit --suites keeps it; an empty list means every
// room or suite.
export function editScope(list: readonly string[], edit: ScopeEdit): string[] | null {
  const value = ('add' in edit ? edit.add : edit.remove).trim()
  if (!value) return null
  if ('add' in edit) {
    if (list.includes(value)) return null
    return [...list, value].sort()
  }
  if (!list.includes(value)) return null
  return list.filter((v) => v !== value)
}

// cpuUseText words the CPUs running work holds against the node's total.
export function cpuUseText(c: Pick<MachineCapacity, 'cpus' | 'cpusUsed'>): string {
  return c.cpus > 0 ? `${c.cpusUsed} of ${count(c.cpus, 'CPU')}` : `${count(c.cpusUsed, 'CPU')} held`
}

// memoryUseText words the memory running work holds against the node's total.
export function memoryUseText(c: Pick<MachineCapacity, 'memoryMiB' | 'memoryMiBUsed'>): string {
  const held = sizeText(c.memoryMiBUsed * 2 ** 20)
  return c.memoryMiB > 0 ? `${held} of ${sizeText(c.memoryMiB * 2 ** 20)}` : `${held} held`
}

// diskLimitText words a node's free disk against what its disk rule keeps. A
// block is a designed line of its own, so this stays a figure.
export function diskLimitText(c: Pick<MachineCapacity, 'diskFree' | 'diskTotal' | 'diskKeeps'>): string {
  if (c.diskTotal === 0) return 'Not probed'
  const free = `${sizeText(c.diskFree)} free`
  return c.diskKeeps ? `${free} · keeps ${c.diskKeeps}` : free
}

// workHoldsText words what one unit of work holds or asks of its node.
export function workHoldsText(item: Pick<MachineWorkItem, 'cpus' | 'memoryMiB'>): string {
  return `${count(item.cpus, 'CPU')} · ${sizeText(item.memoryMiB * 2 ** 20)}`
}

// workKindText words the kind of a unit of work.
export function workKindText(kind: MachineWorkItem['kind']): string {
  return kind === 'job' ? 'Job' : 'Check'
}

// workWhoText names the session and room a unit of work belongs to. It leaves
// out a name the engine could not resolve, and never shows an ID in its place.
export function workWhoText(item: Pick<MachineWorkItem, 'sessionName' | 'roomName'>): string {
  return [item.sessionName, item.roomName].filter(Boolean).join(' · ')
}
