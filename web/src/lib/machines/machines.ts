import type { MachineCIRun, MachineCapabilities, MachineDetail, MachineLoad, MachineRole, MachineStatus, MachineSummary } from '../api/machine.ts'

// roleLabel is the short name a role chip shows.
export function roleLabel(role: MachineRole): string {
  return role === 'ci' ? 'CI' : role
}

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
// what it has: no CI role means no CI part, and a remote machine reports no
// live figure until the probe reads it. A CI machine always shows its slots,
// with the used count once the CI work reports one.
export function summaryParts(machine: MachineSummary): string[] {
  const parts: string[] = []
  if (machine.agents !== undefined) parts.push(count(machine.agents, 'agent'))
  // A disabled machine takes no CI work, so it reports no slots at all. The
  // noun follows the total: "CI 1 slot", "CI 6/16 slots".
  if (machine.roles.includes('ci')) {
    const slots = machine.ci ? `CI ${machine.ci.used}/${count(machine.ci.slots, 'slot')}` : `CI ${count(machine.ciSlots, 'slot')}`
    parts.push(machine.enabled ? slots : 'CI off')
  }
  if (machine.computers !== undefined) parts.push(count(machine.computers, 'computer'))
  return parts
}

// summaryText is the row's one line of counts.
export function summaryText(machine: MachineSummary): string {
  return summaryParts(machine).join(' · ')
}

// ciRunText names one CI run the way its row shows it: the PR's number and
// title, so a reader never looks a number up.
export function ciRunText(run: Pick<MachineCIRun, 'number' | 'title'>): string {
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

// sizeText words a byte count the way the load card does: "30.5 GiB".
export function sizeText(bytes: number): string {
  return `${(bytes / GIB).toFixed(1)} GiB`
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
