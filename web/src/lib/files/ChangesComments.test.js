import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { domNames, installDom, load } from '../prs/prDetailHarness.ts'

const diff = ['diff --git a/a.go b/a.go', '--- a/a.go', '+++ b/a.go', '@@ -1,2 +1,3 @@', ' keep', '+added', ' tail', ''].join('\n')

test('Changes of a session opens a comment box and sends the comment to that session', async () => {
  const sent = []
  const dom = installDom({ changeAPI: {
    gitStatus: async () => ({ root: '/work/ses', branch: 'feat/x', files: [{ path: 'a.go', unstaged: true }], commits: [] }),
    gitDiff: async (_room, _session, path) => ({ text: path ? '' : diff }),
    sendWorktreeComments: async (...args) => { sent.push(args) },
  } })
  const built = await load({ component: '../files/Changes', api: 'export const api = globalThis.changeAPI' })
  const target = document.getElementById('app')
  const until = async (predicate) => {
    for (let i = 0; i < 60; i++) { built.flushSync(); if (predicate()) return; await new Promise((resolve) => setImmediate(resolve)) }
    assert.fail('Changes did not reach the expected state')
  }
  let instance
  try {
    instance = built.mount(built.default, { target, props: { roomId: 'room', session: 'ses_1', sessionName: 'worker' } })
    await until(() => target.querySelectorAll('.ln').length === 3)
    const plus = target.querySelectorAll('.addc')[1]
    assert.ok(plus, 'a + on a diff line')
    plus.click(); built.flushSync()
    const box = target.querySelector('.box textarea')
    box.value = 'Name this'; box.dispatchEvent(new Event('input', { bubbles: true })); built.flushSync()
    target.querySelector('.go').click(); built.flushSync()
    assert.equal(sent.length, 0, 'nothing is sent before Send')
    const send = target.querySelector('.send')
    assert.equal(send.textContent.trim(), 'Send 1 comment to worker')
    send.click()
    await until(() => sent.length === 1)
    assert.deepEqual(sent[0], ['room', 'ses_1', [{ file: 'a.go', line: 2, quote: '+added', text: 'Name this' }]])
    await until(() => !target.querySelector('.sendbar'))
  } finally {
    if (instance) await built.unmount(instance)
    dom.window.close()
    for (const name of [...domNames, 'changeAPI']) delete globalThis[name]
    await rm(built.dir, { recursive: true, force: true })
  }
})
