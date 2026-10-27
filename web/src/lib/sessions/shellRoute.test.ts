import { test } from 'node:test'
import assert from 'node:assert/strict'
import { shellDestination } from './shellRoute.ts'
import { ROOT_SESSION } from '../ui/route.ts'

test('a shell log opens on the owning session Shell tab, or on root when it has no session', () => {
  const job = 'sh_20261017-100000-aaaaaaaa'
  const session = 'ses_20261017-100000-cccccccc'
  const members = [{ id: session, address: 'room:lead' }]
  assert.deepEqual(shellDestination(job, session, members), { session, jobId: job, tab: 'shell' })
  assert.deepEqual(shellDestination(job, 'room:lead', members), { session, jobId: job, tab: 'shell' })
  assert.deepEqual(shellDestination(job, '', members), { session: ROOT_SESSION, jobId: job, tab: 'shell' })
  assert.deepEqual(shellDestination(job, 'hook', members), { session: ROOT_SESSION, jobId: job, tab: 'shell' })
})
