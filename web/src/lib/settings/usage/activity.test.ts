import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createTestServer } from '../../../../test/ssr-server.ts'
import { call, runFunctions } from '../../../../test/svelte-source.ts'
import { need } from '../../../../test/need.ts'
import { requestGate } from './state.ts'
import { rangeBounds, roleRows, modelRows, dayBars, metricText, costText, cacheRate, tokenTotal, type ActivityRollup, type CachePaired, type Cost, type DayModelRow, type Metric, type RoleModelRow, type UsageRow } from './activity.ts'

const metric = (tokens: number | null, completeTurns = 0, unknownTurns = 0, partialTurns = 0): Metric => ({ tokens, completeTurns, partialTurns, unknownTurns })
const cost = (usd: number | null, completeTurns = 0, unknownTurns = 0): Cost => ({ usd, completeTurns, partialTurns: 0, unknownTurns })
const usage = (extra: Partial<UsageRow> = {}): UsageRow => ({ turns: 10, wallMs: 3600e3, input: metric(1000, 10), cached: metric(800, 10), output: metric(100, 10), cost: cost(2.5, 10), cachePaired: { input: 1000, cached: 800, turns: 10 }, ...extra })
const dayRow = (day: string, model: string, extra: Partial<UsageRow> = {}): DayModelRow => ({ ...usage(extra), day, model })
const roleRow = (role: string, model: string, extra: Partial<UsageRow> = {}): RoleModelRow => ({ ...usage(extra), role, model })
// Muse reports no usage: every metric is null and every turn is unknown.
const noUsage: Partial<UsageRow> = { input: metric(null, 0, 4), cached: metric(null, 0, 4), output: metric(null, 0, 4), cost: cost(null, 0, 4), cachePaired: { input: 0, cached: 0, turns: 0 }, turns: 4 }
const snapshot: ActivityRollup = {
  totals: usage({ turns: 14, input: metric(1000, 10, 4), cached: metric(800, 10, 4), output: metric(100, 10, 4), cost: cost(2.5, 10, 4) }),
  byDayModel: [
    dayRow('2026-10-18', 'claude-opus-5-5'),
    dayRow('2026-10-19', 'claude-opus-5-5'),
    dayRow('2026-10-19', 'muse-wonder-full-1.0', noUsage),
  ],
  byRoleModel: [
    roleRow('subagent', 'claude-opus-5-5'),
    roleRow('subagent', 'muse-wonder-full-1.0', noUsage),
    roleRow('reviewer', 'claude-opus-5-5', { turns: 3 }),
    roleRow('keeper', 'claude-opus-5-5', { turns: 1 }),
  ],
}

// The server starts outside the test clock; the render test measures only the render.
const vite = await createTestServer({ after })

test('period buttons become UTC day bounds that end now', () => {
  const now = new Date('2026-10-19T15:30:00Z')
  assert.deepEqual(rangeBounds('today', now), { from: '2026-10-19T00:00:00.000Z', to: now.toISOString() })
  assert.equal(rangeBounds('7d', now).from, '2026-10-13T00:00:00.000Z')
  assert.equal(rangeBounds('30d', now).from, '2026-09-20T00:00:00.000Z')
  assert.equal(rangeBounds('90d', now).from, '2026-07-22T00:00:00.000Z')
})

test('the tab reads usageRollup for its room and period, and no other usage call', async () => {
  const calls: unknown[][] = []
  const context: { snapshot: ActivityRollup | null; scope: string; [name: string]: unknown } = {
    mounted: true, gate: requestGate(), loading: false, error: '', snapshot: null, scope: 'room-a', period: '7d', rangeBounds,
    api: { usageRollup: async (...args: unknown[]) => { calls.push(args); return snapshot }, localUsage: async () => { throw new Error('scans turn files') } },
  }
  runFunctions(readFileSync(new URL('./LocalActivity.svelte', import.meta.url), 'utf8'), ['load'], context)
  await call(context, 'load')
  assert.equal(calls.length, 1)
  const [room, from, to] = need(calls[0])
  assert.equal(room, 'room-a')
  assert.match(String(from), /^\d{4}-\d\d-\d\dT00:00:00\.000Z$/)
  assert.ok(Math.abs(Date.parse(String(to)) - Date.now()) < 5000)
  assert.equal(context.snapshot, snapshot)
  context.scope = ''
  await call(context, 'load')
  await call(context, 'load')
  assert.equal(need(calls[1])[0], '')
})

test('the pane header Refresh reloads the tab that is showing, and the tab has no Refresh of its own', () => {
  const reloads: string[] = []
  const context = { tab: 'activity', activity: { reload: () => reloads.push('activity') }, load: (hard: boolean) => reloads.push('accounts:' + hard) }
  runFunctions(readFileSync(new URL('./UsagePane.svelte', import.meta.url), 'utf8'), ['refresh'], context)
  call(context, 'refresh')
  context.tab = 'accounts'; call(context, 'refresh')
  assert.deepEqual(reloads, ['activity', 'accounts:true'])
  assert.doesNotMatch(readFileSync(new URL('./LocalActivity.svelte', import.meta.url), 'utf8'), />Refresh</)
})

test('unknown stays unknown: a lower bound, a no-data count, never 0 or $0.00', () => {
  assert.equal(metricText(metric(1000, 10, 4)), '≥ 1.0k')
  assert.equal(metricText(metric(1000, 10)), '1.0k')
  assert.equal(metricText(metric(null, 0, 4)), 'unknown')
  assert.equal(metricText(metric(0, 5)), '0')
  assert.equal(costText(cost(2.5, 10, 4)), '≥ $2.50')
  assert.equal(costText(cost(2.5, 10)), '$2.50')
  assert.equal(costText(cost(null, 0, 4)), 'unknown')
})

test('model rows sum the roles; a model with no reports reads unknown', () => {
  const rows = modelRows(snapshot.byRoleModel)
  const opus = need(rows.find((r) => r.model === 'claude-opus-5-5')), muse = need(rows.find((r) => r.model === 'muse-wonder-full-1.0'))
  assert.equal(opus.turns, 14)
  assert.equal(opus.input.tokens, 3000)
  assert.deepEqual(opus.cachePaired, { input: 3000, cached: 2400, turns: 30 })
  assert.equal(cacheRate(opus), 80)
  assert.equal(muse.input.tokens, null)
  assert.equal(muse.input.unknownTurns, 4)
})

test('roles group as the mockup: reviewers, services are keepers', () => {
  const rows = roleRows(snapshot.byRoleModel)
  assert.deepEqual(rows.map((r) => r.label), ['Subagents', 'Reviewers', 'Services', 'Root'])
  assert.equal(need(rows[1]).turns, 3)
  assert.equal(need(rows[3]).turns, 0)
})

test('day bars keep a model on its colour and count no-data turns per day', () => {
  const { days, series } = dayBars(snapshot.byDayModel)
  assert.deepEqual(days.map((d) => d.day), ['2026-10-18', '2026-10-19'])
  assert.equal(need(days[1]).unknownTurns, 4)
  assert.equal(need(days[0]).unknownTurns, 0)
  assert.equal(need(days[1]).total, 1100)
  assert.equal(need(series.find((s) => s.label === 'Opus')).slot, 1)
})

test('cache rate uses only the turns that reported both, and is absent when none did', () => {
  const row = (cachedMetric: Metric, paired: CachePaired) => ({ cached: cachedMetric, cachePaired: paired })
  assert.equal(cacheRate(row(metric(900, 3, 4), { input: 1000, cached: 900, turns: 3 })), 90)
  assert.equal(cacheRate(row(metric(900, 3, 4), { input: 0, cached: 0, turns: 0 })), null)
  assert.equal(cacheRate(row(metric(null, 0, 4), { input: 0, cached: 0, turns: 0 })), null)
  assert.equal(metricText(metric(9.05e9, 5000, 72)), '≥ 9.05B')
})

test('combined tokens keep the coverage of both input and output', () => {
  const input = metric(100, 1)
  assert.equal(metricText(tokenTotal(input, metric(null, 0, 1))), '≥ 100')
  assert.equal(metricText(tokenTotal(input, metric(50, 1, 0, 1))), '≥ 150')
  assert.equal(metricText(tokenTotal(input, metric(50, 1))), '150')
  assert.equal(metricText(tokenTotal(metric(null, 0, 1), metric(null, 0, 1))), 'unknown')
  const { days } = dayBars([dayRow('2026-10-19', 'claude-opus-5-5', { output: metric(null, 0, 1) })])
  assert.equal(need(days[0]).partial, true)
})

test('turns saved before the harness and model were recorded say so', async () => {
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Report } = await vite.ssrLoadModule('/src/lib/settings/usage/ActivityReport.svelte')
  const legacy = roleRow('subagent', '', noUsage)
  const html = render(Report, { props: { snapshot: { ...snapshot, byRoleModel: [...snapshot.byRoleModel, legacy] } } }).body
  assert.match(html, /Not recorded/)
  assert.doesNotMatch(html, /Model not recorded/)
})
