// Browser fixture only: a usage rollup shaped like a room with about 10,000
// turns, in the same form as api.usageRollup. Not imported by the app.
import type { Cost, DayModelRow, Metric, RoleModelRow, UsageRow } from '../src/lib/settings/accounts/activity.ts'
import type { UsageWindow } from '../src/lib/api/account.ts'
export const fixtureWindow = (id: string, label: string, used: number, resetsAt: string): UsageWindow => ({ id, label, usedPercent: used, resetsAt })

// One model's average turn: its token counts, cache share, cost flag and the share of turns without usage data.
interface Shape {
  model: string
  turns: number
  input: number
  cache: number
  output: number
  cost: number
  noData: number
  sec: number
}
const MODELS: Shape[] = [
  { model: 'claude-opus-5-5', turns: 26, input: 210e3, cache: 0.86, output: 5.2e3, cost: 1, noData: 0.01, sec: 44 },
  { model: 'claude-sonnet-5-5', turns: 62, input: 150e3, cache: 0.84, output: 3.9e3, cost: 1, noData: 0.01, sec: 31 },
  { model: 'gpt-6.1-sol', turns: 88, input: 120e3, cache: 0.78, output: 3.1e3, cost: 0, noData: 0.04, sec: 38 },
  { model: 'mimo-v2.6-pro', turns: 70, input: 95e3, cache: 0.7, output: 2.6e3, cost: 1, noData: 0.02, sec: 27 },
  { model: 'grok-4.7', turns: 30, input: 80e3, cache: 0.6, output: 2.4e3, cost: 0, noData: 0.03, sec: 35 },
  { model: 'claude-haiku-5-5', turns: 20, input: 60e3, cache: 0.88, output: 1.4e3, cost: 1, noData: 0.01, sec: 12 },
  { model: 'gpt-6-luna', turns: 14, input: 90e3, cache: 0.74, output: 1.8e3, cost: 0, noData: 0.04, sec: 22 },
  { model: 'muse-wonder-full-1.0', turns: 25, input: 0, cache: 0, output: 0, cost: 0, noData: 1, sec: 50 },
]
const ROLE_SHARE: Record<string, number> = { subagent: 0.62, reviewer: 0.2, keeper: 0.1, root: 0.08 }
const metric = (sum: number, known: number, noData: number): Metric => ({ tokens: known > 0 ? Math.round(sum) : null, completeTurns: known, partialTurns: 0, unknownTurns: noData })
const cost = (usd: number, known: number, noData: number): Cost => ({ usd: known > 0 ? usd : null, completeTurns: known, partialTurns: 0, unknownTurns: noData })
function group(m: Shape, turns: number): UsageRow {
  const noData = Math.round(turns * m.noData), known = turns - noData
  const input = known * m.input, cached = input * m.cache, output = known * m.output
  return { turns, wallMs: turns * m.sec * 1000, input: metric(input, m.input ? known : 0, m.input ? noData : turns), cached: metric(cached, m.cache ? known : 0, m.cache ? noData : turns), output: metric(output, m.output ? known : 0, m.output ? noData : turns), cachePaired: { input: m.cache ? input : 0, cached: m.cache ? cached : 0, turns: m.cache ? known : 0 }, cost: cost((input - cached) / 1e6 * 3 + cached / 1e6 * 0.3 + output / 1e6 * 15, m.cost ? known : 0, m.cost ? noData : turns) }
}
export function rollupFixture(days = 30, now = new Date()) {
  let seed = 7
  const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647
  const byDayModel: DayModelRow[] = [], byRoleModel: RoleModelRow[] = []
  const dayList = [...Array(days)].map((_, i) => new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - (days - 1 - i))))
  const total = new Map<Shape, number>()
  for (const d of dayList) for (const m of MODELS) {
    const weekend = [0, 6].includes(d.getUTCDay()) ? 0.35 : 1
    const turns = Math.round(m.turns * weekend * (0.7 + 0.6 * rnd()))
    byDayModel.push({ ...group(m, turns), model: m.model, day: d.toISOString().slice(0, 10) })
    total.set(m, (total.get(m) ?? 0) + turns)
  }
  for (const [role, share] of Object.entries(ROLE_SHARE)) for (const m of MODELS) byRoleModel.push({ ...group(m, Math.round((total.get(m) ?? 0) * share)), model: m.model, role })
  const sum = (rows: UsageRow[], pick: (row: UsageRow) => number | null) => rows.reduce((a, r) => a + (pick(r) ?? 0), 0)
  function agg(key: 'input' | 'cached' | 'output'): Metric {
    return { tokens: byDayModel.some((r) => r[key].tokens !== null) ? sum(byDayModel, (r) => r[key].tokens) : null, completeTurns: sum(byDayModel, (r) => r[key].completeTurns), partialTurns: 0, unknownTurns: sum(byDayModel, (r) => r[key].unknownTurns) }
  }
  const costTotal: Cost = { usd: byDayModel.some((r) => r.cost.usd !== null) ? sum(byDayModel, (r) => r.cost.usd) : null, completeTurns: sum(byDayModel, (r) => r.cost.completeTurns), partialTurns: 0, unknownTurns: sum(byDayModel, (r) => r.cost.unknownTurns) }
  const totals: UsageRow = { turns: sum(byDayModel, (r) => r.turns), wallMs: sum(byDayModel, (r) => r.wallMs), input: agg('input'), cached: agg('cached'), output: agg('output'), cost: costTotal, cachePaired: { input: sum(byDayModel, (r) => r.cachePaired.input), cached: sum(byDayModel, (r) => r.cachePaired.cached), turns: sum(byDayModel, (r) => r.cachePaired.turns) } }
  return { roomId: '', from: dayList[0]?.toISOString().slice(0, 10), to: dayList.at(-1)?.toISOString().slice(0, 10), totals, byDayModel, byRoleModel }
}
