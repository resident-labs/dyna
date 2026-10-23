import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'
import { element } from '../../../test/need.ts'

const diff = ['diff --git a/a.go b/a.go', '--- a/a.go', '+++ b/a.go', '@@ -1 +1,2 @@', ' keep', '+added', ''].join('\n')
const fixtures = await Promise.all([
  { label: 'Worktree', component: '../review/ReviewChanges', props: { roomId: 'room', session: 'worker-id', sessionName: 'worker' } },
  { label: 'PR', component: 'PRFiles', props: { roomId: 'room', number: 17, authorSession: 'worker-id', author: 'worker' } },
].map(async (fixture) => ({ ...fixture, code: await bundle({ component: fixture.component, api: 'export const api = globalThis.reviewAPI' }) })))

for (const fixture of fixtures) test(fixture.label + ' line comments reach the author session and stay pending after a failed send', async () => {
  const sent: unknown[][] = []
  let fail = true
  const dom = installDom({ reviewAPI: {
    gitStatus: async () => ({ root: '/work/worker', branch: 'feature', worktree: true, files: [{ path: 'a.go', unstaged: true }], commits: [] }),
    gitDiff: async (_room: string, _session: string, _path: string, staged: boolean) => ({ text: staged ? '' : diff }),
    githubDiff: async () => ({ text: diff }),
    sendToSession: async (...args: unknown[]) => { sent.push(args); if (fail) throw new Error('Offline') },
  } })
  const built = await importBundle(fixture.code, fixture.component)
  let instance
  try {
    const target = element(document, '#app')
    instance = built.mount(built.default, { target, props: fixture.props })
    built.flushSync()
    await new Promise(setImmediate)
    built.flushSync()
    element<HTMLButtonElement>(target, '[aria-label="Comment on line 2"]').click()
    built.flushSync()
    const field = element<HTMLTextAreaElement>(target, '.box textarea')
    field.value = 'Check this value.'
    field.dispatchEvent(new Event('input', { bubbles: true }))
    built.flushSync()
    element<HTMLButtonElement>(target, '.go').click()
    built.flushSync()
    assert.equal(sent.length, 0)
    element<HTMLButtonElement>(target, '.send').click()
    await new Promise(setImmediate)
    built.flushSync()
    assert.match(element(target, '.sendbar').textContent ?? '', /Offline/)
    fail = false
    element<HTMLButtonElement>(target, '.send').click()
    await new Promise(setImmediate)
    built.flushSync()
    assert.deepEqual(sent, [
      ['worker-id', 'a.go:2\n> +added\n\nCheck this value.'],
      ['worker-id', 'a.go:2\n> +added\n\nCheck this value.'],
    ])
    assert.equal(target.querySelector('.sendbar'), null)
  } finally {
    if (instance) await built.unmount(instance)
    dom.window.close()
    for (const name of [...domNames, 'reviewAPI']) Reflect.deleteProperty(globalThis, name)
    await rm(built.dir, { recursive: true, force: true })
  }
})
