import { test } from 'node:test'
import assert from 'node:assert/strict'
import { commandLine, editedArgv, emptyForm, requestActions, splitCommand, type RequestApi } from './termRequest.ts'
import type { TermInfo } from '../api/terminal.ts'

function fake() {
  const calls: unknown[][] = []
  const api: RequestApi = {
    run: async (...a) => { calls.push(['run', ...a]); return { id: 't1' } as TermInfo },
    preview: async () => { calls.push(['preview']); return 'one\ntwo' },
    report: async (...a) => { calls.push(['report', ...a]) },
  }
  return { calls, api }
}

test('making the actions calls nothing; only Run starts the command, with argv only when edited', async () => {
  const { calls, api } = fake()
  const act = requestActions(api)
  assert.deepEqual(calls, [])
  const same = editedArgv(['gh', 'auth', 'login', '--scopes', 'a b'], "gh auth login --scopes 'a b'")
  assert.deepEqual(same, { argv: null })
  await act.run(null)
  const changed = editedArgv(['gh', 'auth', 'login'], 'gh auth status')
  assert.deepEqual(changed, { argv: ['gh', 'auth', 'status'] })
  await act.run(['gh', 'auth', 'status'])
  assert.deepEqual(calls, [['run', null], ['run', ['gh', 'auth', 'status']]])
})

test('sharing lines is off by default and sends only lines the owner saw', async () => {
  const { calls, api } = fake()
  const act = requestActions(api)
  const f = emptyForm()
  assert.equal(f.share, false)
  await act.report('done', { ...f, note: ' logged in ' })
  await act.report('done', { ...f, share: true }) // ticked, never shown
  await act.report('failed', { ...f, share: true, lines: 'one\ntwo' })
  await act.decline(' not now ')
  assert.deepEqual(calls, [['report', 'done', 'logged in', false], ['report', 'done', '', false], ['report', 'failed', '', true], ['report', 'declined', 'not now', false]])
})

test('a command shows with quotes and reads back the same; a broken edit is an error', () => {
  const argv = ['echo', "it's", 'a b', '', '$HOME']
  const line = commandLine(argv)
  assert.deepEqual(splitCommand(line), { argv })
  assert.deepEqual(splitCommand('a "b \\"c\\"" d\\ e'), { argv: ['a', 'b "c"', 'd e'] })
  assert.ok('error' in splitCommand("echo 'open"))
  assert.ok('error' in splitCommand('   '))
})
