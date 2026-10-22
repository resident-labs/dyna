import test from 'node:test'
import assert from 'node:assert/strict'
import { allowanceLabel, accountLabel, remainingPercent, percent, requestGate, needsSignIn, accountStatus, observation, shortLabel, windowReset, usageGroups, unreadText, harnessName } from './state.ts'
import { usageAccount } from '../../../../test/account-fixtures.ts'
import { need } from '../../../../test/need.ts'
import type { UsageWindow } from '../../api/account.ts'

// A window with the fields a test leaves out.
const win = (fields: Partial<UsageWindow> = {}): UsageWindow => ({ id: '', label: '', ...fields })
// Engine data the types rule out, such as a string percent or a null. The helpers must still read it.
const raw = (text: string) => JSON.parse(text)

test('missing allowance is unknown while an explicitly exhausted bucket is zero', () => {
  assert.equal(remainingPercent({}), null)
  assert.equal(remainingPercent(raw('{"remainingPercent":null}')), null)
  assert.equal(remainingPercent({ usedPercent: 100 }), 0)
  assert.equal(remainingPercent({ usedPercent: 120 }), 0)
  assert.equal(remainingPercent({ remainingPercent: 0 }), 0)
  assert.equal(remainingPercent({ usedPercent: 12.5 }), 87.5)
  assert.equal(remainingPercent({ remainingPercent: NaN }), null)
  assert.equal(remainingPercent(raw('{"usedPercent":"10"}')), null)
  assert.equal(percent(null), 'Not reported')
})
test('needsSignIn and quotaBlocked status reporting', () => {
  assert.equal(needsSignIn({ needsLogin: true }), true)
  assert.equal(needsSignIn({ status: 'needs_auth' }), true)
  assert.equal(needsSignIn({ status: 'ready' }), false)
  const at = Date.parse('2026-10-18T12:00:00Z')
  assert.match(accountStatus({ status: 'ready', quotaBlocked: true, blockingQuotaWindows: ['seven_day'], quotaBlockedUntil: '2026-10-18T13:00:00Z' }, '', at), /Weekly limit reached/)
  assert.match(observation({ source: 'OAuth', refreshMessage: 'Refreshed by Dyna' }), /Refreshed by Dyna/)
})
test('a limit whose blocking windows have all reset stops being reported on an open page', () => {
  const at = Date.parse('2026-10-18T14:00:00Z')
  // The host marked the account blocked for a session window that has reset since.
  assert.equal(accountStatus({ harness: 'codex', status: 'ready', quotaBlocked: true, blockingQuotaWindows: ['five_hour'], quotaBlockedUntil: '2026-10-18T13:00:00Z' }, '', at), 'Plan not reported')
  // A second window is still exhausted, and its reset is ahead: keep its name.
  assert.match(accountStatus({ harness: 'codex', status: 'ready', quotaBlocked: true, blockingQuotaWindows: ['five_hour', 'seven_day'], quotaBlockedUntil: '2026-10-18T13:00:00Z', windows: [
    win({ id: 'five_hour', usedPercent: 100, resetsAt: '2026-10-18T13:00:00Z' }),
    win({ id: 'seven_day', usedPercent: 100, resetsAt: '2026-10-20T13:00:00Z' }),
  ] }, '', at), /Weekly limit reached/)
  // The same reading with both resets passed reports no limit at all.
  assert.doesNotMatch(accountStatus({ harness: 'codex', status: 'ready', quotaBlocked: true, blockingQuotaWindows: ['five_hour', 'seven_day'], quotaBlockedUntil: '2026-10-20T13:00:00Z', windows: [
    win({ id: 'five_hour', usedPercent: 100, resetsAt: '2026-10-18T13:00:00Z' }),
    win({ id: 'seven_day', usedPercent: 100, resetsAt: '2026-10-20T13:00:00Z' }),
  ] }, '', Date.parse('2026-10-21T09:00:00Z')), /limit reached/)
  // A window that is only partly used never blocks, whatever the host said.
  assert.equal(accountStatus({ harness: 'codex', status: 'ready', quotaBlocked: true, windows: [win({ id: 'seven_day', usedPercent: 40, resetsAt: '2026-10-20T13:00:00Z' })] }, '', at), 'Plan not reported')
})
test('a delayed response cannot replace a newer request, even for the same identity', async () => {
  const gate = requestGate()
  let resolve: (value: string) => void = () => {}
  const pending = new Promise<string>((r) => { resolve = r })
  const old = gate.next()
  const result = pending.then((value) => gate.current(old) ? value : null)
  gate.invalidate()
  const current = gate.next()
  resolve('old account')
  assert.equal(await result, null)
  assert.equal(gate.current(current), true)
})

test('allowance arithmetic preserves reported values without guessing from reset times', () => {
  const resetsAt = '2000-01-01T00:00:00Z'
  assert.equal(remainingPercent({ usedPercent: 100, resetsAt }), 0)
  assert.equal(remainingPercent({ resetsAt }), null)
  assert.equal(remainingPercent({ usedPercent: 100, resetsAt: 'invalid' }), 0)
})

test('OpenCode default labels identify the provider and preserve custom account names', () => {
  for (const [integration, label] of [['opencode-go', 'OpenCode Go'], ['opencode', 'OpenCode Zen']]) {
    assert.equal(accountLabel({ harness: 'opencode', integration, label: 'OpenCode account' }), label)
    assert.equal(accountLabel({ harness: 'opencode', integration, label: '' }), label)
    assert.equal(accountLabel({ harness: 'opencode', integration, label: 'Work account' }), 'Work account')
  }
  assert.equal(accountLabel({ harness: 'codex', label: 'OpenCode account' }), 'OpenCode account')
})


test('short window labels name the model group and the period only', () => {
  const cases: Record<string, string> = {
    'Gemini Models: Weekly Limit Remaining (weekly)': 'Gemini weekly',
    'Gemini Models: Five Hour Limit Remaining (5h)': 'Gemini 5-hour',
    'Claude and GPT models: Weekly Limit Remaining (weekly)': 'Claude/GPT weekly',
    'Session (5-hour)': '5-hour',
    'Weekly (7-day)': 'Weekly',
    'Fable Weekly': 'Fable weekly',
    'Weekly usage': 'Weekly',
    'Monthly credits': 'Monthly credits',
  }
  for (const [label, short] of Object.entries(cases)) assert.equal(shortLabel(label), short, label)
})

 test('observation shows a failed usage read with cached windows', () => {
  assert.match(observation({ usageError: 'Provider unavailable', windows: [win({ usedPercent: 20 })] }), /Provider unavailable/)
})

test('subscription groups use the reported plan and preserve account readings', () => {
  const weekly = usageAccount({ id: 'g1', harness: 'grok', plan: { id: 'x-subscription', label: '' }, windows: [win({ id: 'weekly', resetsAt: '2099-01-01T00:00:00Z' })] })
  const other = { ...weekly, id: 'g2' }
  const legacy = usageAccount({ id: 'old', harness: 'grok', plan: { id: 'grok-legacy', label: '' } })
  const unknown = usageAccount({ id: 'unknown', harness: 'grok' })
  const groups = usageGroups([weekly, other, legacy, unknown])
  assert.deepEqual(groups.map(g => g.id), ['x-subscription', 'grok-legacy', 'unknown'])
  const [subscription, old] = groups
  assert.deepEqual(need(subscription).accounts, [weekly, other])
  assert.equal(need(need(subscription).accounts[0]).windows, weekly.windows)
  assert.equal(need(subscription).name, 'X Subscription (weekly)')
  assert.equal(need(old).name, 'Legacy (monthly)')
  assert.equal(unreadText(legacy), 'Legacy plan, usage not read')
  assert.equal(harnessName('grok'), 'Grok')
  assert.deepEqual(usageGroups([]), [])
})

test('window allowance shows the provider reason without inventing a percent', () => {
  assert.equal(allowanceLabel({ unavailableReason: 'Grok does not report remaining allowance.' }), 'Grok does not report remaining allowance.')
})

test('rate limited observations explain the cached reading without an unavailable prefix', () => {
  const message = 'rate limited; showing the reading from 2026-10-19T08:00:00Z'
  const detail = observation({ usageError: message, rateLimitedUntil: '2026-10-19T08:05:00Z' })
  assert.ok(detail.includes(message))
  assert.ok(!detail.includes('Usage unavailable:'))
  assert.ok(observation({ usageError: 'provider unavailable' }).includes('Usage unavailable: provider unavailable'))
})

 test('idle window explains a passed reset without a new model call', () => {
 const window = win({ label: 'Session (5-hour) (idle)', usedPercent: 21, resetsAt: '2020-01-01T00:00:00Z' })
 assert.equal(remainingPercent(window), 79)
 assert.match(windowReset(window), /^Idle since /)
 assert.match(windowReset({ ...window, label: 'Weekly' }), /^Reset /)
 })

test('OpenCode omits an unreported plan while preserving real plans and account warnings', () => {
  const account = { harness: 'opencode', status: 'ready' }
  assert.equal(accountStatus(account), '')
  assert.equal(accountStatus({ ...account, plan: { id: 'unknown', label: '' } }), '')
  assert.equal(accountStatus({ ...account, plan: { id: '', label: 'Reported plan' } }), 'Reported plan')
  assert.equal(accountStatus({ ...account, needsLogin: true }), 'Needs sign-in')
  assert.equal(accountStatus({ ...account, quotaBlocked: true }), 'Session limit reached')
})
