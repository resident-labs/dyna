import { test } from 'node:test'
import assert from 'node:assert/strict'
import { sections, rowOf, shown, runningWork, visibleTabs, roomTabs } from './workViews.ts'

test('the sidebar has nine rows in Work and Machine; Room settings is a modal, not a row', () => {
  assert.deepEqual(sections.map((s) => [s.label, s.rows.map((r) => r.id)]), [
    ['Work', ['questions', 'goals', 'runs', 'services', 'visuals']],
    ['Machine', ['code', 'library', 'terminal', 'computers']],
  ])
})

test('work views resolve to their sidebar rows; PRs belongs to the room', () => {
  const rows = Object.fromEntries(['questions', 'goals', 'runs', 'jobs', 'services', 'visuals', 'files', 'github', 'issues', 'cihealth', 'library', 'terminal', 'computers'].map((v) => [v, rowOf(v)?.id]))
  assert.deepEqual(rows, { questions: 'questions', goals: 'goals', runs: 'runs', jobs: 'runs', services: 'services', visuals: 'visuals', files: 'code', github: undefined, issues: 'code', cihealth: 'code', library: 'library', terminal: 'terminal', computers: 'computers' })
  assert.deepEqual(rowOf('files')?.tabs.map((t) => t.id), ['files', 'issues', 'cihealth'], 'the code header has no Changes tab')
  assert.equal(rowOf('changes'), undefined)
  for (const view of ['settings', 'rules', 'room-general', 'room-hooks']) assert.equal(rowOf(view), undefined, view)
  assert.equal(rowOf('general'), undefined)
  assert.equal(rowOf('channel:ses_a'), undefined)
})

test('rows that need a repo, a terminal or computers hide without them', () => {
  const ids = (have: { repo?: boolean; terminal?: boolean; computers?: boolean }) => shown(sections.flatMap((s) => s.rows), have).map((r) => r.id)
  assert.deepEqual(ids({}), ['questions', 'goals', 'runs', 'services', 'visuals'])
  assert.deepEqual(ids({ repo: true, terminal: true, computers: true }), ['questions', 'goals', 'runs', 'services', 'visuals', 'code', 'library', 'terminal', 'computers'])
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

test('the code row has Files, Issues and CI health tabs, and no Changes or Library tab', () => {
  assert.deepEqual(rowOf('issues')?.tabs.map((t) => t.label), ['Files', 'Issues', 'CI health'])
  assert.equal(rowOf('cihealth')?.id, 'code')
})

test('Library is its own Machine row after code, repo-scoped, and the library view still resolves', () => {
  const machine = sections.find((s) => s.label === 'Machine')?.rows ?? []
  assert.deepEqual(machine.map((r) => r.id), ['code', 'library', 'terminal', 'computers'])
  assert.equal(rowOf('library')?.repo, true)
  assert.deepEqual(rowOf('library')?.tabs.map((t) => t.id), ['library'])
  assert.ok(!shown(machine, {}).some((r) => r.id === 'library'), 'hidden in a room without a repository')
})

test('room tabs keep the Messages and PR deep-link view IDs outside Code', () => {
  assert.deepEqual(roomTabs, [{ id: 'general', label: 'Messages' }, { id: 'github', label: 'PRs' }])
  assert.equal(rowOf('github'), undefined)
})
