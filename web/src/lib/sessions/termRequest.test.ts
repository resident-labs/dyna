import { test } from 'node:test'
import assert from 'node:assert/strict'
import { autoVerdict, commandLine, followUpText, requestActions, sentLine, type RequestApi, type SentReport } from './termRequest.ts'
import type { TermInfo } from '../api/terminal.ts'

function fake() {
  const calls: unknown[][] = []
  const api: RequestApi = {
    run: async () => { calls.push(['run']); return { id: 't1' } as TermInfo },
    preview: async () => { calls.push(['preview']); return 'one\ntwo' },
    report: async (...a) => { calls.push(['report', ...a]) },
  }
  return { calls, api }
}

test('making the actions calls nothing; Run starts the filed command and Decline reports the reason', async () => {
  const { calls, api } = fake()
  const act = requestActions(api)
  assert.deepEqual(calls, [])
  await act.run()
  await act.decline(' not now ')
  assert.deepEqual(calls, [['run'], ['report', 'declined', 'not now', false]])
})

test('a command shows with quotes', () => {
  assert.equal(commandLine(['echo', "it's", 'a b', '', '$HOME']), `echo 'it'\\''s' 'a b' '' '$HOME'`)
})

test('the sent line and the follow-up say what went to the filer', () => {
  const s: SentReport = { requestId: 'q1', askerName: 'root', verdict: autoVerdict(0), code: 0 }
  assert.equal(sentLine(s), 'Sent to root: done (exit 0)')
  assert.equal(sentLine({ ...s, verdict: 'failed', code: 3 }), 'Sent to root: failed (exit 3)')
  assert.equal(sentLine({ requestId: 'q1', askerName: 'root', verdict: 'done' }), 'Sent to root: done')
  assert.equal(followUpText(s, ' logged in ', null), '@root Follow-up on terminal request q1 (done).\n\nlogged in')
  assert.match(followUpText(s, '', 'one\ntwo\n'), /```\none\ntwo\n```$/)
})
