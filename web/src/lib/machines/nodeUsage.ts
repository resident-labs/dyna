import type { MachineDetail } from '../api/machine.ts'
import { ago } from '../ui/format.ts'
import type { Tone } from '../ui/tone.ts'

export const usageThresholds = { warn: 80, bad: 95 } as const

export function usageTone(percent: number | null | undefined): Tone {
  if (percent == null) return 'neutral'
  return percent >= usageThresholds.bad ? 'danger' : percent >= usageThresholds.warn ? 'warning' : 'neutral'
}

export function diskTone(free: number | undefined, floor: number | undefined): Tone {
  return free !== undefined && floor !== undefined && free < floor ? 'warning' : 'neutral'
}

export function freshness(machine: Pick<MachineDetail, 'local' | 'seenAt' | 'status'>): string {
  if (machine.local) return 'CPU and memory live · every 5 seconds'
  if (!machine.seenAt) return 'Not probed yet'
  const age = ago(machine.seenAt)
  return machine.status === 'offline'
    ? `Last read ${age} · last probe failed`
    : `Probed ${age} · every 5 min`
}
