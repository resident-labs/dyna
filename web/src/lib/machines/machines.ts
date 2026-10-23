import type { MachineCIRun, MachineRole, MachineStatus, MachineSummary } from '../api/machine.ts'

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
  if (machine.roles.includes('ci')) parts.push(machine.ci ? `CI ${machine.ci.used}/${machine.ci.slots} slots` : `CI ${machine.ciSlots} slots`)
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
