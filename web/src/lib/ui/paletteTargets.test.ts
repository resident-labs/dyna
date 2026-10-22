import { test } from 'node:test'
import assert from 'node:assert/strict'
import { findTargets, paletteTargets, PER_KIND } from './paletteTargets.ts'

const sessions = [
  { id: 'owner', name: 'you', owner: true },
  { id: 'ses_20261017-100000-aaaaaaaa', name: 'web-navigation', status: 'working', profile: 'opus' },
  { id: 'ses_20261017-100000-bbbbbbbb', name: 'reviewer', closed: true },
]
const runs = [{ id: 'wf_20261017-100000-cccccccc', name: 'review-batch', status: 'running', parent: 'web-navigation' }]
const changes = [
  { changeId: 'chg_1', number: 1570, title: 'Make the URL follow navigation', branch: 'feat/url-navigation', repo: 'o/r' },
  { changeId: 'chg_2', kind: 'local', branch: 'fix/local-thing' },
]
const all = paletteTargets(sessions, runs, changes)

test('the palette finds sessions, runs and PRs by name, hint and branch', () => {
  assert.deepEqual(findTargets('navigation', all).map((t) => [t.kind, t.label]), [
    ['session', 'web-navigation'],
    ['run', 'review-batch'], // its parent session's name is in the hint
    ['pr', 'Make the URL follow navigation'],
  ])
  assert.deepEqual(findTargets('opus', all).map((t) => t.key), ['ses_20261017-100000-aaaaaaaa'])
  assert.deepEqual(findTargets('local-thing', all).map((t) => t.kind === 'pr' && t.pr), ['chg_2'], 'a local change opens by its change ID')
  assert.deepEqual(findTargets('1570', all).map((t) => t.kind === 'pr' && t.pr), ['1570'], 'a GitHub PR opens by its number')
  assert.equal(findTargets('closed', all)[0]?.label, 'reviewer')
})

test('the owner is not a session row, and an empty query shows no targets', () => {
  assert.equal(all.some((t) => t.kind === 'session' && t.session.owner), false)
  assert.deepEqual(findTargets('  ', all), [])
})

test('a broad query shows at most PER_KIND rows of each kind', () => {
  const many = paletteTargets(Array.from({ length: 20 }, (_, i) => ({ id: `s${i}`, name: `worker-${i}` })), runs, changes)
  const found = findTargets('e', many)
  assert.equal(found.filter((t) => t.kind === 'session').length, PER_KIND)
  assert.ok(found.some((t) => t.kind === 'run'))
  assert.ok(found.some((t) => t.kind === 'pr'))
})
