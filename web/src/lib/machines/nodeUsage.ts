import type { MachineDetail } from '../api/machine.ts'
import { ago } from '../ui/format.ts'

export type UsageTone = 'muted' | 'warn' | 'bad'
export const usageThresholds = { warn: 80, bad: 95 } as const

export function usageTone(percent: number | null | undefined): UsageTone {
  if (percent == null) return 'muted'
  return percent >= usageThresholds.bad ? 'bad' : percent >= usageThresholds.warn ? 'warn' : 'muted'
}

export function diskTone(free: number | undefined, floor: number | undefined): UsageTone {
  return free !== undefined && floor !== undefined && free < floor ? 'warn' : 'muted'
}

export function freshness(machine: Pick<MachineDetail, 'local' | 'seenAt' | 'status'>): string {
  if (machine.local) return 'CPU and memory live · every 5 seconds'
  if (!machine.seenAt) return 'Not probed yet'
  const age = ago(machine.seenAt)
  return machine.status === 'offline'
    ? `Last read ${age} · last probe failed`
    : `Probed ${age} · every 5 min`
}
