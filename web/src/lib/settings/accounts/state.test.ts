import test from 'node:test'
import assert from 'node:assert/strict'
import { allowanceLabel, accountLabel, accountsOf, accountStatus, money, needsSignIn, observation, percent, problemText, providerName, remainingPercent, reposLabel, requestGate, shortLabel, usageUnavailableText, selectionText, windowReset } from './state.ts'
import { account } from '../../../../test/account-fixtures.ts'
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
  assert.match(observation({ source: 'OAuth', observedAt: '2026-10-18T12:00:00Z' }), /Checked/)
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

test('an account without a label is named by its provider, and a provider has a name', () => {
  assert.equal(providerName('anthropic'), 'Anthropic')
  assert.equal(providerName('zai'), 'Z.ai')
  assert.equal(providerName(''), 'No provider')
  assert.equal(accountLabel({ provider: 'deepseek', harness: 'pi' }), 'DeepSeek')
  assert.equal(accountLabel({ provider: 'deepseek', label: 'Work', harness: 'pi' }), 'Work')
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

test('window allowance shows the provider reason without inventing a percent', () => {
  assert.equal(allowanceLabel({ unavailableReason: 'Grok does not report remaining allowance.' }), 'Grok does not report remaining allowance.')
})

test('idle window explains a passed reset without a new model call', () => {
  const window = win({ label: 'Session (5-hour) (idle)', usedPercent: 21, resetsAt: '2020-01-01T00:00:00Z' })
  assert.equal(remainingPercent(window), 79)
  assert.match(windowReset(window), /^Idle since /)
  assert.match(windowReset({ ...window, label: 'Weekly' }), /^Reset /)
})

test('OpenCode omits an unreported plan while preserving real plans and account warnings', () => {
  const row = { harness: 'opencode', status: 'ready' }
  assert.equal(accountStatus(row), '')
  assert.equal(accountStatus({ ...row, plan: { id: 'unknown', label: '' } }), '')
  assert.equal(accountStatus({ ...row, plan: { id: '', label: 'Reported plan' } }), 'Reported plan')
  assert.equal(accountStatus({ ...row, needsLogin: true }), 'Needs sign-in')
  assert.equal(accountStatus({ ...row, quotaBlocked: true }), 'Session limit reached')
})

test('an account without readable usage says why in words, never raw engine text', () => {
  assert.equal(usageUnavailableText({ kind: 'api_key', usageError: 'provider said 403' }), 'Usage unavailable for an API key')
  assert.equal(usageUnavailableText({ kind: 'oauth' }), 'Usage unavailable')
  assert.equal(usageUnavailableText({ kind: 'oauth', needsLogin: true }), 'Sign in to read usage')
  assert.equal(usageUnavailableText({ kind: 'oauth', usageReason: 'expired' }), 'Sign in again to read usage')
  assert.equal(usageUnavailableText({ kind: 'api_key', usageReason: 'secret' }), 'Reader output rejected: contained a secret')
  assert.equal(usageUnavailableText({ kind: 'api_key', usageReason: 'provider-error' }), 'Usage is temporarily unavailable')
  assert.equal(usageUnavailableText({ kind: 'api_key', usageReason: 'rate-limited' }), 'Usage is rate limited. Try again later')
  assert.equal(usageUnavailableText({ kind: 'api_key', usageReason: 'no-windows' }), 'The provider reported no usage windows')
  assert.equal(usageUnavailableText({ kind: 'api_key', provider: 'meta-muse', usageReason: 'not-readable' }), 'Dyna cannot read Muse plan usage from this API key. Sign in to Muse with Pi\'s OAuth sign-in to read it.')
  assert.equal(usageUnavailableText({ kind: 'api_key', provider: 'zai', usageReason: 'not-readable' }), 'This provider does not publish usage that Dyna can read.')
  assert.equal(observation({ usageError: 'provider said 403', rateLimitedUntil: '2000-01-01T00:00:00Z' }), '')
})

test('a balance is money, not a percent', () => {
  assert.equal(money(53.78, 'USD'), '$53.78')
  assert.equal(money(0, 'USD'), '$0.00')
  assert.equal(money(12, 'CNY'), '12 CNY')
  assert.equal(money(null, 'USD'), 'Not reported')
  assert.equal(allowanceLabel({ balance: 53.78, unit: 'USD' }), '')
})

test('the cards word the repo limit, the selection and a skipped saved row', () => {
  assert.equal(reposLabel({ repos: null }), 'All repos')
  assert.equal(reposLabel({ repos: ['github.com/me/one'] }), '1 repo only')
  assert.equal(reposLabel({ repos: ['a', 'b'] }), '2 repos only')
  assert.equal(selectionText({ ...account({ id: 'a', harness: 'pi' }), activeHere: true }, false), 'In use')
  assert.equal(selectionText({ ...account({ id: 'a', harness: 'pi' }), activeHere: false, active: true }, true), 'Global choice')
  assert.equal(selectionText({ ...account({ id: 'a', harness: 'pi' }), activeHere: false, active: false }, true), '')
  assert.match(problemText({ id: 'x', label: 'Work', reason: 'missing-home' }), /^Work: its saved home directory is gone\.$/)
  assert.match(problemText({ id: 'x', reason: 'unreadable' }), /^A saved account: Dyna could not read this saved account\.$/)
})

test('accountsOf flattens the provider groups in their engine order', () => {
  const work = account({ id: 'w', harness: 'pi' })
  const home = account({ id: 'h', harness: 'pi' })
  assert.deepEqual(accountsOf({ providers: [{ provider: 'anthropic', accounts: [{ ...work, activeHere: true }] }, { provider: 'zai', accounts: [{ ...home, activeHere: false }] }] }).map((a) => a.id), ['w', 'h'])
  assert.deepEqual(accountsOf(null), [])
  assert.deepEqual(accountsOf({ providers: null }), [])
})
