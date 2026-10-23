// How a provider reset reads on a Home notice. A window minutes away reads in
// minutes, a window hours away in hours, and a window days away in days and
// hours: a reset six days out must not look like one this evening.
export function resetLine(remainingMs: number): string {
  if (!Number.isFinite(remainingMs)) return ''
  if (remainingMs <= 0) return 'resets now'
  const minutes = Math.round(remainingMs / 60000)
  if (minutes < 1) return 'resets in 1m'
  if (minutes < 90) return `resets in ${minutes}m`
  const hours = Math.floor(remainingMs / 3600000)
  if (hours < 24) return `resets in ${hours}h`
  const days = Math.floor(hours / 24)
  const rest = hours % 24
  return rest ? `resets in ${days}d ${rest}h` : `resets in ${days}d`
}
