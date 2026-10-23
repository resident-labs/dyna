// Presentation of the usage rollup. The host sums; this shapes rows for the
// Dyna activity tab. A null total is unknown and never reads as zero.
const DAY = 864e5

export interface Metric { tokens: number | null; completeTurns: number; partialTurns: number; unknownTurns: number }
export interface Cost { usd: number | null; completeTurns: number; partialTurns: number; unknownTurns: number }
export interface CachePaired { input: number; cached: number; turns: number }
export interface UsageRow {
  turns: number
  wallMs: number
  input: Metric
  cached: Metric
  output: Metric
  cost: Cost
  cachePaired: CachePaired
}
export interface RoleModelRow extends UsageRow { role: string; model: string }
export interface DayModelRow extends UsageRow { day: string; model: string }
export type Period = keyof typeof PERIOD_DAYS
// ActivityRollup is what the usage rollup returns for a range; from and to are UTC days.
export interface ActivityRollup {
  from?: string
  to?: string
  totals: UsageRow
  byRoleModel: RoleModelRow[]
  byDayModel: DayModelRow[]
}

// rangeBounds turns a period button into RFC 3339 bounds. The rollup counts
// whole UTC days, so a range starts at the UTC midnight of its first day.
const PERIOD_DAYS = { today: 1, '7d': 7, '30d': 30, '90d': 90 }
export function rangeBounds(period: Period, now = new Date()) {
  const start = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate()) - (PERIOD_DAYS[period] - 1) * DAY
  return { from: new Date(start).toISOString(), to: now.toISOString() }
}

export function tokenText(n: number) {
  const fixed = (v: number, d: number) => v.toFixed(d).replace(/\.0$/, '.0')
  if (n < 1e3) return String(n)
  if (n < 1e6) return fixed(n / 1e3, n < 1e4 ? 1 : 0) + 'k'
  if (n < 1e9) return fixed(n / 1e6, n < 1e8 ? 1 : 0) + 'M'
  return fixed(n / 1e9, 2) + 'B'
}
const lowerBound = (m: { partialTurns: number; unknownTurns: number }) => (m.partialTurns > 0 || m.unknownTurns > 0 ? '≥ ' : '')
export function metricText(m: Metric) {
  return m.tokens === null || m.tokens === undefined ? 'unknown' : lowerBound(m) + tokenText(m.tokens)
}
export function costText(c: Cost) {
  return c.usd === null || c.usd === undefined ? 'unknown' : lowerBound(c) + '$' + c.usd.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}
export function durationText(ms: number) {
  const h = ms / 36e5
  return h >= 1 ? `${h.toFixed(h >= 100 ? 0 : 1)} h` : `${Math.round(ms / 6e4)} min`
}
// cacheRate is cached / input over the turns that reported both, which the
// rollup sums apart from the per-metric totals. Without such a turn it is unknown.
export function cacheRate(row: { cachePaired: CachePaired }) {
  const paired = row.cachePaired
  return paired.turns > 0 && paired.input > 0 ? Math.min(100, Math.round(100 * paired.cached / paired.input)) : null
}

// tokenTotal adds input and output and keeps the coverage of both, so a
// complete input beside an unknown output stays a lower bound.
export function tokenTotal(input: Metric, output: Metric): Metric {
  const known = input.tokens !== null || output.tokens !== null
  return { tokens: known ? (input.tokens ?? 0) + (output.tokens ?? 0) : null, completeTurns: Math.min(input.completeTurns, output.completeTurns), partialTurns: input.partialTurns + output.partialTurns, unknownTurns: input.unknownTurns + output.unknownTurns }
}

const plus = (a: number | null, b: number | null) => (b === null ? a : (a ?? 0) + b)
function sumTokens(list: UsageRow[], key: 'input' | 'cached' | 'output'): Metric {
  const out: Metric = { tokens: null, completeTurns: 0, partialTurns: 0, unknownTurns: 0 }
  for (const row of list) {
    const m = row[key]
    out.tokens = plus(out.tokens, m.tokens)
    out.completeTurns += m.completeTurns; out.partialTurns += m.partialTurns; out.unknownTurns += m.unknownTurns
  }
  return out
}
function sumCost(list: UsageRow[]): Cost {
  const out: Cost = { usd: null, completeTurns: 0, partialTurns: 0, unknownTurns: 0 }
  for (const row of list) {
    out.usd = plus(out.usd, row.cost.usd)
    out.completeTurns += row.cost.completeTurns; out.partialTurns += row.cost.partialTurns; out.unknownTurns += row.cost.unknownTurns
  }
  return out
}
function sumRows(list: UsageRow[]): UsageRow {
  return {
    turns: list.reduce((a, r) => a + r.turns, 0), wallMs: list.reduce((a, r) => a + r.wallMs, 0),
    input: sumTokens(list, 'input'), cached: sumTokens(list, 'cached'), output: sumTokens(list, 'output'), cost: sumCost(list),
    cachePaired: { input: list.reduce((a, r) => a + r.cachePaired.input, 0), cached: list.reduce((a, r) => a + r.cachePaired.cached, 0), turns: list.reduce((a, r) => a + r.cachePaired.turns, 0) },
  }
}

// A model keeps its colour slot by family, so a filter never repaints it.
export const SLOTS: [string, number][] = [['Opus', 1], ['Sonnet', 2], ['GPT', 3], ['MiMo', 4], ['Grok', 5], ['Other', 0]]
const FAMILIES: [RegExp, number][] = [[/opus/i, 1], [/sonnet/i, 2], [/gpt/i, 3], [/mimo/i, 4], [/grok/i, 5]]
export const slotOf = (model: string) => FAMILIES.find(([re]) => re.test(model))?.[1] ?? 0

export function modelRows(byRoleModel: RoleModelRow[]) {
  const groups = Map.groupBy(byRoleModel, (r) => r.model)
  return [...groups].map(([model, rows]) => ({ model, slot: slotOf(model), ...sumRows(rows) })).sort((a, b) => (b.input.tokens ?? -1) - (a.input.tokens ?? -1) || b.turns - a.turns)
}

const ROLES: { label: string; note: string; roles: string[] }[] = [
  { label: 'Subagents', note: 'workers started by agents', roles: ['subagent'] },
  { label: 'Reviewers', note: 'PR review', roles: ['reviewer'] },
  { label: 'Services', note: 'background services', roles: [] },
  { label: 'Root', note: 'room root sessions', roles: ['root'] },
]
export function roleRows(byRoleModel: RoleModelRow[]) {
  const named = ROLES.flatMap((g) => g.roles)
  return ROLES.map((g) => {
    const rows = byRoleModel.filter((r) => g.roles.includes(r.role) || (!g.roles.length && !named.includes(r.role)))
    const mix = [...Map.groupBy(rows, (r) => slotOf(r.model))].map(([slot, list]) => ({ slot, tokens: list.reduce((a, r) => a + (r.input.tokens ?? 0) + (r.output.tokens ?? 0), 0) })).filter((m) => m.tokens > 0).sort((a, b) => a.slot - b.slot)
    return { label: g.label, note: g.note, ...sumRows(rows), mix }
  })
}

const dayKey = (t: number) => new Date(t).toISOString().slice(0, 10)
// dayBars sums known input + output tokens per UTC day and colour slot. Turns
// with no usage data are counted apart per day, never given a height.
export function dayBars(byDayModel: DayModelRow[], from?: string, to?: string) {
  interface Day { day: string; total: number; unknownTurns: number; partial: boolean; turns: number; segments: Map<number, number> }
  const days = new Map<string, Day>()
  const get = (day: string): Day => {
    let d = days.get(day)
    if (!d) days.set(day, (d = { day, total: 0, unknownTurns: 0, partial: false, turns: 0, segments: new Map() }))
    return d
  }
  if (from && to) for (let t = Date.parse(from + 'T00:00:00Z'); t <= Date.parse(to + 'T00:00:00Z'); t += DAY) get(dayKey(t))
  const totals = new Map<number, number>()
  for (const r of byDayModel) {
    const d = get(r.day), slot = slotOf(r.model), known = (r.input.tokens ?? 0) + (r.output.tokens ?? 0)
    d.total += known; d.turns += r.turns; d.unknownTurns += r.input.unknownTurns
    d.partial ||= r.input.partialTurns + r.output.partialTurns + r.output.unknownTurns > 0
    d.segments.set(slot, (d.segments.get(slot) ?? 0) + known)
    totals.set(slot, (totals.get(slot) ?? 0) + known)
  }
  const out = [...days.values()].sort((a, b) => (a.day < b.day ? -1 : 1)).map((d) => ({ ...d, segments: [...d.segments].filter(([, v]) => v > 0).sort((a, b) => a[0] - b[0]).map(([slot, tokens]) => ({ slot, tokens })) }))
  const series = SLOTS.filter(([, slot]) => (totals.get(slot) ?? 0) > 0).map(([label, slot]) => ({ label, slot }))
  return { days: out, series, max: Math.max(0, ...out.map((d) => d.total)) }
}
