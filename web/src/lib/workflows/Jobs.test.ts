import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { errorText } from '../errorText.ts'
import type { ShellLog } from '../api/terminal.ts'

test('job logs retry failed reads, preserve reading position, and reject late responses', async () => {
  const pending: { resolve: (log: ShellLog) => void; reject: (error: Error) => void }[] = []
  const logEl = { scrollHeight: 1000, scrollTop: 5, clientHeight: 300 }
  const state: Record<string, unknown> = {
    sel: 'first', log: null, logError: '', logLoading: false, logRequest: 0, loadingJob: '',
    logEl, follow: false, tick: async () => {}, errorText,
    api: { shellLog: () => new Promise<ShellLog>((resolve, reject) => pending.push({ resolve, reject })) },
  }
  const source = readFileSync(new URL('./Jobs.svelte', import.meta.url), 'utf8')
  runFunctions(source, ['loadLog', 'trackFollow', 'resumeFollowing'], state)
  const first = call(state, 'loadLog')
  await call(state, 'loadLog')
  assert.equal(pending.length, 1, 'a timed refresh waits for the selected job read')
  pending[0]?.reject(new Error('offline'))
  await first
  assert.equal(state.logError, 'offline')
  const retry = call(state, 'loadLog')
  pending[1]?.resolve({ text: 'first log', truncated: true })
  await retry
  assert.equal(state.logError, '')
  assert.equal(logEl.scrollTop, 5)
  call(state, 'resumeFollowing')
  assert.equal(state.follow, true)
  assert.equal(logEl.scrollTop, 1000)
  logEl.scrollTop = 0
  call(state, 'trackFollow')
  assert.equal(state.follow, false)
  const old = call(state, 'loadLog')
  state.sel = 'second'
  const current = call(state, 'loadLog')
  pending[3]?.resolve({ text: 'second log', truncated: false })
  await current
  pending[2]?.resolve({ text: 'stale first log', truncated: false })
  await old
  assert.equal((state.log as ShellLog).text, 'second log')
  const closing = call(state, 'loadLog')
  state.sel = ''
  pending[4]?.resolve({ text: 'closed selection', truncated: false })
  await closing
  assert.equal(state.logLoading, false, 'a completed read clears loading after Back')
  state.sel = 'second'
  state.log = null
  const reopened = call(state, 'loadLog')
  assert.equal(pending.length, 6, 'reopening the same job starts a new read')
  pending[5]?.resolve({ text: 'reopened log', truncated: false })
  await reopened
  assert.equal((state.log as ShellLog).text, 'reopened log')
})
