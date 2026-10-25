import test from 'node:test'
import assert from 'node:assert/strict'
import { accountRows, updateRows, rootQuestions, allClear } from './home.ts'

test('working accounts with a usage reading are cards; login and quota problems are alert rows', () => {
  const { cards, alerts } = accountRows({ accounts: [
    { id: 'c', harness: 'claude-code', label: 'claude-code', active: true, windows: [{ id: 'five_hour', label: '5h', remainingPercent: 40 }] },
    { id: 'q', harness: 'codex', label: 'quiet', active: true, windows: [] },
    { id: 'n', harness: 'grok', label: 'grok', active: true, windows: [{ id: 'weekly', label: 'Weekly' }] },
    { id: 'o', harness: 'opencode', label: 'argon', active: true, quotaBlocked: true, blockingQuotaWindows: ['seven_day'] },
    { id: 'x', harness: 'codex', label: 'spare', active: false, needsLogin: true },
  ] })
  assert.deepEqual(cards.map((a) => a.id), ['c'])
  assert.deepEqual(alerts.map((a) => [a.id, a.text, a.action]), [
    ['o', 'argon: Weekly limit reached', 'Details'],
  ])
})

test('updates list dyna and installed harnesses with a newer recommended version', () => {
  const runtimes = {
    bundles: [
      { harness: 'claude-code', version: '2.4.1', executable: 'claude', directory: '/home/claude' },
      { harness: 'codex', version: '0.63.2', executable: 'codex', directory: '/home/codex' },
      { harness: 'opencode', version: '1.10.0', executable: 'opencode', directory: '/home/opencode' },
    ],
    selectedVersions: { 'claude-code': '2.4.1', codex: '0.63.2', opencode: '1.10.0' },
    recommendedVersions: [
      { harness: 'claude-code', version: '2.10.0', status: 'resolved', source: 'npm' },
      { harness: 'codex', version: '0.63.2', status: 'resolved', source: 'npm' },
      { harness: 'opencode', version: '1.9.0', status: 'resolved', source: 'npm' },
    ],
  }
  const updates = updateRows({ current: '0.42.0', latest: '0.43.1' }, runtimes)
  assert.deepEqual(updates, [
    { id: 'dyna', name: 'dyna', current: '0.42.0', latest: '0.43.1' },
    { id: 'claude-code', name: 'claude-code', current: '2.4.1', latest: '2.10.0' },
  ])
  assert.deepEqual(updateRows({}, null), [])
})

test('the home screen shows only open questions root asked the human', () => {
  const rooms = [{ id: 'a', name: 'dyna-agent' }, { id: 'b', name: 'old', archived: true }, { id: 'c', name: 'other' }]
  const questions = {
    c: [{ id: 'q1', state: 'open', to: 'owner', asker: 'owner', askerName: 'root', text: 'Merge?' }],
    a: [
      { id: 'q1', state: 'open', to: 'owner', asker: 'owner', askerName: 'root', text: 'Ship it?' },
      { id: 'q2', state: 'open', to: 'owner', asker: 'ses_x', askerName: 'ui' },
      { id: 'q3', state: 'answered', to: 'owner', asker: 'owner', askerName: 'root' },
      { id: 'q4', state: 'open', to: 'owner', asker: 'owner', human: true, askerName: 'you' },
    ],
    b: [{ id: 'q5', state: 'open', to: 'owner', asker: 'owner', askerName: 'root' }],
  }
  // Question IDs are unique only within a room, so the row key names both.
  assert.deepEqual(rootQuestions(rooms, questions).map((r) => [r.key, r.room.id, r.text]), [['a:q1', 'a', 'Ship it?'], ['c:q1', 'c', 'Merge?']])
})

test('home uses the shared grouping and includes known subscriptions without percentages', async () => {
  const { usageGroups } = await import('../settings/usage/state.ts')
  const accounts = [
    { id: 'weekly', harness: 'grok', active: true, plan: { id: 'x-subscription', label: 'X Subscription (weekly)' }, windows: [{ id: 'weekly', label: 'Weekly' }] },
    { id: 'second', harness: 'grok', active: true, plan: { id: 'x-subscription', label: 'X Subscription (weekly)' } },
    { id: 'legacy', harness: 'grok', active: true, plan: { id: 'grok-legacy', label: 'Legacy (monthly)' } },
    { id: 'inactive', harness: 'grok', active: false, plan: { id: 'x-subscription', label: 'X Subscription (weekly)' } },
  ]
  assert.deepEqual(accountRows({ accounts }).cards, usageGroups(accounts.filter(a => a.active)))
})

test('home says all clear only after every source loaded and none needs the owner', () => {
  const empty = { usage: {}, runtimes: {}, questionsLoaded: true, noticesLoaded: true, cards: 0, asked: 0, alerts: 0, updates: 0 }
  assert.equal(allClear(empty), true)
  for (const missing of ['usage', 'runtimes', 'questionsLoaded', 'noticesLoaded']) assert.equal(allClear({ ...empty, [missing]: missing.endsWith('Loaded') ? false : null }), false, `${missing} still loading or failed`)
  for (const busy of ['cards', 'asked', 'alerts', 'updates']) assert.equal(allClear({ ...empty, [busy]: 1 }), false, `${busy} need the owner`)
})
