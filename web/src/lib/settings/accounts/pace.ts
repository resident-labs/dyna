// Pace compares the used share of a window with the share of its time that has passed.
export interface PaceWindow {
  period?: string
  periodStart?: string
  resetsAt?: string
  usedPercent?: number
}

export type PaceStatus = 'ahead' | 'on' | 'under'

export interface Pace {
  status: PaceStatus
  // expectedRemaining is the remaining share an even spend would leave, in percent.
  expectedRemaining: number
}

const MINUTE = 60_000
const DAY = 24 * 60 * MINUTE
// Only a named week has a fixed length. A month depends on the calendar.
const PERIOD_LENGTH: Partial<Record<string, number>> = { week: 7 * DAY }

// windowLength reads the length a reader reports: a start time, or a week period.
// It returns null when the reader reports neither, so no pace is shown.
function windowLength(window: PaceWindow): number | null {
  const end = Date.parse(window.resetsAt ?? '')
  const start = Date.parse(window.periodStart ?? '')
  if (Number.isFinite(start) && Number.isFinite(end) && end > start) return end - start
  return PERIOD_LENGTH[window.period ?? ''] ?? null
}

// paceOf returns null when the window has no reported length, no reading or a passed reset.
export function paceOf(window: PaceWindow, now: number): Pace | null {
  const length = windowLength(window)
  const end = Date.parse(window.resetsAt ?? '')
  if (length === null || window.usedPercent === undefined || !Number.isFinite(end) || end <= now) return null
  const elapsed = Math.min(100, Math.max(0, ((length - (end - now)) / length) * 100))
  return { status: statusOf(window.usedPercent - elapsed), expectedRemaining: 100 - elapsed }
}

function statusOf(gap: number): PaceStatus {
  return gap > 5 ? 'ahead' : gap < -5 ? 'under' : 'on'
}

// poolPace compares the mean used share of a pool's windows with the mean
// elapsed share. Windows with no known length or reading do not count.
export function poolPace(windows: readonly PaceWindow[], now: number): PaceStatus | null {
  let gap = 0, n = 0
  for (const window of windows) {
    const pace = paceOf(window, now)
    if (!pace || window.usedPercent === undefined) continue
    gap += window.usedPercent - (100 - pace.expectedRemaining)
    n++
  }
  return n ? statusOf(gap / n) : null
}

// meterWake is the next time a meter's countdown or pace can change: the next whole minute, or the reset when it comes first.
// It returns NaN once the reset has passed or when no reset is known.
export function meterWake(resetsAt: string | undefined, now: number): number {
  const end = Date.parse(resetsAt ?? '')
  if (!Number.isFinite(end) || end <= now) return NaN
  return Math.min(end, (Math.floor(now / MINUTE) + 1) * MINUTE)
}

// resetsIn words the time left before a reset, such as "1h 40m". It returns '' once the reset has passed.
export function resetsIn(resetsAt: string | undefined, now: number): string {
  const end = Date.parse(resetsAt ?? '')
  if (!Number.isFinite(end) || end <= now) return ''
  const minutes = Math.ceil((end - now) / MINUTE)
  const days = Math.floor(minutes / (24 * 60))
  const hours = Math.floor((minutes % (24 * 60)) / 60)
  const rest = minutes % 60
  if (days) return `${days}d ${hours}h`
  if (hours) return `${hours}h ${rest}m`
  return `${rest}m`
}
