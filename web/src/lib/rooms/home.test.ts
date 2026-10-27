import test from 'node:test'
import assert from 'node:assert/strict'
import { accountRows, updateRows, homeRequests, allClear } from './home.ts'
import { account } from '../../../test/account-fixtures.ts'
import type { ScopeAccount } from '../api/account.ts'

// One row of a provider group, with every account field its type requires.
const row = (fields: Partial<ScopeAccount> & Pick<ScopeAccount, 'id'>): ScopeAccount => ({ ...account({ id: fields.id, harness: 'pi' }), activeHere: true, ...fields })

test('working accounts with a usage reading are cards of their provider; login and quota problems are alert rows', () => {
  const { cards, alerts } = accountRows({ providers: [
    { provider: 'anthropic', accounts: [
      row({ id: 'c', provider: 'anthropic', label: 'Claude work', windows: [{ id: 'five_hour', label: '5h', remainingPercent: 40 }] }),
      row({ id: 'q', provider: 'anthropic', label: 'Claude home', windows: [] }),
    ] },
    { provider: 'xai', accounts: [
      row({ id: 'n', provider: 'xai', label: 'Grok', windows: [{ id: 'weekly', label: 'Weekly' }] }),
    ] },
    { provider: 'meta-muse', accounts: [
      row({ id: 'o', provider: 'meta-muse', label: 'Muse', quotaBlocked: true, blockingQuotaWindows: ['seven_day'] }),
    ] },
    { provider: 'chatgpt', accounts: [
      row({ id: 'x', provider: 'chatgpt', label: 'Spare', activeHere: false, needsLogin: true }),
    ] },
  ] })
  assert.deepEqual(cards.map((group) => [group.provider, group.accounts.map((a) => a.id)]), [['anthropic', ['c']]])
  assert.deepEqual(alerts.map((a) => [a.id, a.text, a.action]), [
    ['o', 'Muse: Weekly limit reached', 'Details'],
  ])
})

test('updates list dyna and installed harnesses with a newer recommended version', () => {
  const runtimes = {
    bundles: [
      { harness: 'claude-code', version: '2.4.1', executable: 'claude', directory: '/home/claude' },
      { harness: 'pi', version: '0.63.2', executable: 'pi', directory: '/home/pi' },
    ],
    selectedVersions: { 'claude-code': '2.4.1', pi: '0.63.2' },
    recommendedVersions: [
      { harness: 'claude-code', version: '2.10.0', status: 'resolved', source: 'npm' },
      { harness: 'pi', version: '0.63.2', status: 'resolved', source: 'npm' },
    ],
  }
  const updates = updateRows({ current: '0.42.0', latest: '0.43.1' }, runtimes)
  assert.deepEqual(updates, [
    { id: 'dyna', name: 'dyna', current: '0.42.0', latest: '0.43.1' },
    { id: 'claude-code', name: 'claude-code', current: '2.4.1', latest: '2.10.0' },
  ])
  assert.deepEqual(updateRows({}, null), [])
})

test('the home screen shows root questions and terminal requests waiting for the owner', () => {
  const rooms = [{ id: 'a', name: 'dyna-agent' }, { id: 'b', name: 'old', archived: true }, { id: 'c', name: 'other' }]
  const questions = {
    c: [{ id: 'q1', state: 'open', to: 'owner', asker: 'owner', askerName: 'root', text: 'Merge?' }],
    a: [
      { id: 'q1', state: 'open', to: 'owner', asker: 'owner', askerName: 'root', text: 'Ship it?' },
      { id: 'q2', state: 'open', to: 'owner', asker: 'ses_x', askerName: 'ui' },
      { id: 'q3', state: 'answered', to: 'owner', asker: 'owner', askerName: 'root' },
      { id: 'q4', state: 'open', to: 'owner', asker: 'owner', human: true, askerName: 'you' },
      { id: 'q6', state: 'open', to: 'owner', asker: 'ses_t', askerName: 'builder', text: 'Sign in', kind: 'terminal' as const, terminal: { stage: 'waiting-owner' } },
      { id: 'q7', state: 'open', to: 'owner', asker: 'ses_t', askerName: 'builder', kind: 'terminal' as const, terminal: { stage: 'waiting-session' } },
    ],
    b: [{ id: 'q5', state: 'open', to: 'owner', asker: 'owner', askerName: 'root' }],
  }
  // Question IDs are unique only within a room, so the row key names both.
  assert.deepEqual(homeRequests(rooms, questions).map((r) => [r.key, r.room.id, r.text]), [['a:q1', 'a', 'Ship it?'], ['a:q6', 'a', 'Sign in'], ['c:q1', 'c', 'Merge?']])
})

test('home keeps a provider card for a known plan that reports a period without a percentage', () => {
  const { cards } = accountRows({ providers: [
    { provider: 'xai', accounts: [
      row({ id: 'weekly', provider: 'xai', plan: { id: 'x-subscription', label: 'X Subscription (weekly)' }, windows: [{ id: 'weekly', label: 'Weekly' }] }),
      row({ id: 'legacy', provider: 'xai', plan: { id: 'grok-legacy', label: 'Legacy (monthly)' } }),
      row({ id: 'inactive', provider: 'xai', activeHere: false, plan: { id: 'x-subscription', label: 'X Subscription (weekly)' } }),
    ] },
  ] })
  assert.deepEqual(cards.map((group) => [group.provider, group.accounts.map((a) => a.id)]), [['xai', ['weekly', 'legacy']]])
})

test('home says all clear only after every source loaded and none needs the owner', () => {
  const empty = { usage: {}, runtimes: {}, questionsLoaded: true, noticesLoaded: true, cards: 0, asked: 0, alerts: 0, updates: 0 }
  assert.equal(allClear(empty), true)
  for (const missing of ['usage', 'runtimes', 'questionsLoaded', 'noticesLoaded']) assert.equal(allClear({ ...empty, [missing]: missing.endsWith('Loaded') ? false : null }), false, `${missing} still loading or failed`)
  for (const busy of ['cards', 'asked', 'alerts', 'updates']) assert.equal(allClear({ ...empty, [busy]: 1 }), false, `${busy} need the owner`)
})
