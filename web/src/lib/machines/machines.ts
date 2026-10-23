import type { MachineRole, MachineStatus, MachineSummary } from '../api/machine.ts'

// roleLabel is the short name a role chip shows.
export function roleLabel(role: MachineRole): string {
  return role === 'ci' ? 'CI' : role
}

// statusText words a machine's status for a title or a row.
export function statusText(status: MachineStatus): string {
  return status === 'online' ? 'Online' : status === 'off' ? 'Off' : 'Not probed'
}

// count is one named figure: "3 agents", "1 computer".
export function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? '' : 's'}`
}

// summaryParts lists the counts a machine row shows. A machine reports only
// what it has: no CI slots means no CI part, and a remote machine reports no
// live figures until the probe reads them.
export function summaryParts(machine: MachineSummary): string[] {
  const parts: string[] = []
  if (machine.agents !== undefined) parts.push(count(machine.agents, 'agent'))
  if (machine.ci) parts.push(`CI ${machine.ci.used}/${machine.ci.slots} slots`)
  if (machine.computers !== undefined) parts.push(count(machine.computers, 'computer'))
  return parts
}

// summaryText is the row's one line of counts.
export function summaryText(machine: MachineSummary): string {
  return summaryParts(machine).join(' · ')
}

// statusCounts counts machines by status, in the order the subline shows them.
export function statusCounts(machines: readonly MachineSummary[]): { status: MachineStatus; n: number }[] {
  const of = (status: MachineStatus) => machines.filter((m) => m.status === status).length
  return [
    { status: 'online', n: of('online') },
    { status: 'off', n: of('off') },
    { status: 'unknown', n: of('unknown') },
  ]
}

// machinesSub is the Machines header's subline: "2 online · 1 off". It leaves
// out a count of zero, and says so when there is no machine at all.
export function machinesSub(machines: readonly MachineSummary[] | null): string {
  if (!machines) return ''
  if (!machines.length) return 'No machines yet'
  const words: Record<MachineStatus, string> = { online: 'online', off: 'off', unknown: 'not probed' }
  return statusCounts(machines)
    .filter((c) => c.n > 0)
    .map((c) => `${c.n} ${words[c.status]}`)
    .join(' · ')
}

// loadPct is a machine's CPU use for its load bar. A machine without a figure
// has an empty bar, not one at zero.
export function loadPct(machine: MachineSummary): number | null {
  return machine.cpu === undefined ? null : Math.max(0, Math.min(100, machine.cpu))
}

// routeText names how the engine reaches a machine.
export function routeText(machine: Pick<MachineSummary, 'local'> & { ssh: string }): string {
  if (machine.local) return 'This machine'
  return machine.ssh || 'No route yet'
}
