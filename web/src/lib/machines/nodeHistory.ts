import type { NodeHistoryPoint } from '../api/machine.ts'

export function historySegments(points: readonly NodeHistoryPoint[], now: number): { cpu: string; memory: string }[] {
  const lines: { cpu: string; memory: string }[] = []
  const first = points[0], last = points[points.length - 1]
  if (!first || !last) return lines
  const end = Math.min(now, Date.parse(last.at))
  const start = Math.max(now - 86400_000, Date.parse(first.at))
  if (!(end > start)) return lines
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1], b = points[i]
    if (!a || !b) continue
    const ta = Date.parse(a.at), tb = Date.parse(b.at)
    if (tb - ta > 10 * 60_000 || tb <= ta) continue
    const x = (t: number) => Math.max(0, Math.min(100, (t - start) / (end - start) * 100))
    const y = (n: number) => 100 - Math.max(0, Math.min(100, n))
    lines.push({
      cpu: a.cpuPercent != null && b.cpuPercent != null ? `${x(ta)},${y(a.cpuPercent)} ${x(tb)},${y(b.cpuPercent)}` : '',
      memory: a.memoryPercent != null && b.memoryPercent != null ? `${x(ta)},${y(a.memoryPercent)} ${x(tb)},${y(b.memoryPercent)}` : '',
    })
  }
  return lines
}
