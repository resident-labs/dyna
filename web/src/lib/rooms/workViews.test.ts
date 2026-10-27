import { test } from 'node:test'
import assert from 'node:assert/strict'
import { sections, rowOf, shown, runningWork, visibleTabs } from './workViews.ts'

test('the sidebar has eight rows in Work and Machine; Room settings is a modal, not a row', () => {
  assert.deepEqual(sections.map((s) => [s.label, s.rows.map((r) => r.id)]), [
    ['Work', ['requests', 'goals', 'runs', 'services', 'visuals', 'library']],
    ['Machine', ['computers']],
  ])
})

test('work views resolve to their sidebar rows; PRs and Issues belong to the room', () => {
  const rows = Object.fromEntries(['requests', 'questions', 'goals', 'runs', 'jobs', 'services', 'visuals', 'files', 'github', 'issues', 'library', 'terminal', 'computers'].map((v) => [v, rowOf(v)?.id]))
  assert.deepEqual(rows, { requests: 'requests', questions: 'requests', goals: 'goals', runs: 'runs', jobs: 'runs', services: 'services', visuals: 'visuals', files: undefined, github: undefined, issues: undefined, library: 'library', terminal: undefined, computers: 'computers' })
  assert.equal(rowOf('files'), undefined)
  assert.equal(rowOf('changes'), undefined)
  for (const view of ['settings', 'rules', 'room-general', 'room-hooks']) assert.equal(rowOf(view), undefined, view)
  assert.equal(rowOf('general'), undefined)
  assert.equal(rowOf('channel:ses_a'), undefined)
})

test('rows that need a repo or computers hide without them', () => {
  const ids = (have: { repo?: boolean; computers?: boolean }) => shown(sections.flatMap((s) => s.rows), have).map((r) => r.id)
  assert.deepEqual(ids({}), ['requests', 'goals', 'runs', 'services', 'visuals'])
  assert.deepEqual(ids({ repo: true, computers: true }), ['requests', 'goals', 'runs', 'services', 'visuals', 'library', 'computers'])
})

test('the runs count is running workflows only, never a total and never shell jobs', () => {
  const runs = { total: 14, running: 1, runs: [{ status: 'running' }, { status: 'ok' }] }
  assert.equal(runningWork(runs), 1)
  assert.equal(runningWork({ running: 0 }), 0)
  assert.equal(runningWork(null), 0)
})

test('the runs header shows no Shell jobs tab, but a job log stays under the runs row', () => {
  assert.equal(visibleTabs(rowOf('runs')), null)
  assert.equal(rowOf('jobs')?.id, 'runs')
})

test('Library is the last Work row, repo-scoped, and the library view still resolves', () => {
  const work = sections.find((s) => s.label === 'Work')?.rows ?? []
  assert.equal(work.at(-1)?.id, 'library')
  assert.equal(rowOf('library')?.repo, true)
  assert.deepEqual(rowOf('library')?.tabs.map((t) => t.id), ['library'])
  assert.ok(!shown(work, {}).some((r) => r.id === 'library'), 'hidden in a room without a repository')
})

test('Issues has no room row of its own', () => {
  // It is the review workspace's fourth tab, beside Files, Changes and PRs.
  assert.equal(rowOf('github'), undefined)
  assert.equal(rowOf('issues'), undefined)
})
