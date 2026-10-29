import test from 'node:test'
import assert from 'node:assert/strict'
import { ciRunDetail, ciRunText } from './machines.ts'
import type { MachineCIRun } from '../api/machine.ts'

const run = (over: Partial<MachineCIRun> = {}): MachineCIRun => ({
  machine: 'hermes',
  number: 272,
  kind: 'pull-request',
  title: 'Show running local CI per machine',
  url: 'https://github.com/resident-labs/dyna/pull/272',
  trial: false,
  started: '2026-10-23T17:00:00Z',
  ...over,
})

test('a CI run row names the PR, not the number alone', () => {
  assert.equal(ciRunText(run()), '#272 Show running local CI per machine')
  assert.equal(ciRunText(run({ title: '' })), '#272', 'a run without a title still names its PR')
})

test('a local branch run names its change, not a PR number', () => {
  assert.equal(ciRunText(run({ kind: 'local-change', number: 0, title: 'Add parser bounds', url: '' })), 'Add parser bounds')
  assert.equal(
    ciRunText(run({ kind: 'local-change', number: 0, title: '', branch: 'fix/parser-bounds', url: '' })),
    'fix/parser-bounds',
    'a change without a title names its branch',
  )
  assert.equal(ciRunText(run({ kind: 'local-change', number: 0, title: '', url: '' })), 'Local branch change')
  assert.doesNotMatch(ciRunText(run({ kind: 'local-change', number: 0, title: 'Add parser bounds', url: '' })), /#0/)
})

test('an integration sync run names the sync, not a PR number', () => {
  assert.equal(ciRunText(run({ kind: 'sync', number: 0, title: '', url: '' })), 'Integration sync')
})

test('a CI run detail lists the suites the run prepared', () => {
  assert.equal(ciRunDetail(run()), '', 'a run that prepared no suite has no second line')
  assert.equal(ciRunDetail(run({ suites: ['go', 'cross-build'] })), 'go · cross-build')
})
