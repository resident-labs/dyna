import test from 'node:test'
import assert from 'node:assert/strict'
import { runningShells } from './shells.ts'
import type { ShellJob } from './shellBlock.ts'

const job = (id: string, owner: string | undefined, state: ShellJob['state']): ShellJob => ({ id, owner, command: ['bash', '-c', id], cwd: '/', state, code: 0, started: '2026-10-20T12:00:00Z' })

test('runningShells keeps the running and starting jobs a session owns, by id or address', () => {
  const jobs = [job('a', 'ses_1', 'running'), job('b', 'room:me', 'starting'), job('c', 'ses_1', 'exited'), job('d', 'ses_2', 'running'), job('e', undefined, 'running'), job('f', 'ses_1', 'lost')]
  assert.deepEqual(runningShells(jobs, { id: 'ses_1', address: 'room:me' }).map((j) => j.id), ['a', 'b'])
})

test('runningShells is empty without jobs or a member', () => {
  assert.deepEqual(runningShells(null, { id: 'ses_1' }), [])
  assert.deepEqual(runningShells([job('a', 'ses_1', 'running')], null), [])
  assert.deepEqual(runningShells([job('a', 'ses_1', 'running')], {}), [])
})
