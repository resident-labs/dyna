import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { need, element } from '../../../test/need.ts'
import { installDom, domNames } from './prDetailHarness.ts'
import type { HeldComment } from '../api/change.ts'

test('owner-only held comments render as text and resolve in the right room without stale reappearance', async () => {
  const first: HeldComment = { roomId: 'a', key: 'repo#7', number: 7, id: 'issue:19', commenter: 'outside', body: '<img src=x onerror=alert(1)>\n**quoted**', url: '' }
  const second = { ...first, roomId: 'b' }
  const calls: unknown[][] = []
  let read: () => Promise<HeldComment[]> = async () => [first, second]
  let fail = true
  const dom = installDom({
    heldRead: (room: string) => { calls.push(['read', room]); return read() },
    heldAction: async (action: string, ...args: string[]) => {
      calls.push([action, ...args])
      if (fail) throw new Error('Owner required')
    },
  })
  const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'held-comment-test-'))
  const built = await build({
    bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'],
    stdin: {
      contents: `export { default as Notice } from './HeldCommentNotice.svelte';
        export { heldComments, refreshHeldComments } from './heldComments.ts';
        export { mount, unmount, flushSync } from 'svelte'; export { get } from 'svelte/store';`,
      resolveDir: new URL('.', import.meta.url).pathname,
    },
    plugins: [svelteEsbuildPlugin(), { name: 'held-api', setup(builder) {
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({
        contents: `export const api = {
          heldPRComments: room => globalThis.heldRead(room),
          admitPRComment: (...args) => globalThis.heldAction('admit', ...args),
          dismissPRComment: (...args) => globalThis.heldAction('dismiss', ...args),
        }; export const openExternal = () => {};`, loader: 'js', resolveDir: resolve(dir),
      }))
    } }],
  })
  const file = join(dir, 'test.mjs')
  await writeFile(file, need(built.outputFiles?.[0]).contents)
  const m = await import(pathToFileURL(file).href)
  const mounted: unknown[] = []
  try {
    const a = document.createElement('div')
    const b = document.createElement('div')
    document.body.append(a, b)
    await m.refreshHeldComments()
    assert.deepEqual(calls[0], ['read', ''])
    for (const [target, roomId] of [[a, 'a'], [b, 'b']] as const) {
      mounted.push(m.mount(m.Notice, { target, props: { roomId, prKey: first.key, commentId: first.id } }))
    }
    m.flushSync()
    assert.equal(element(a, 'blockquote').textContent, first.body)
    assert.equal(a.querySelector('img'), null)
    element<HTMLButtonElement>(a, '.dismiss').click()
    await Promise.resolve()
    await Promise.resolve()
    m.flushSync()
    assert.match(element(a, '[role="alert"]').textContent ?? '', /Owner required/)
    assert.equal(m.get(m.heldComments).comments.length, 2)

    let finishRead: (items: HeldComment[]) => void = () => {}
    read = () => new Promise((done) => { finishRead = done })
    const staleRead = m.refreshHeldComments()
    fail = false
    element<HTMLButtonElement>(a, '.dismiss').click()
    await Promise.resolve()
    await Promise.resolve()
    m.flushSync()
    assert.deepEqual(calls.at(-1), ['dismiss', 'a', 'repo#7', 'issue:19'])
    assert.equal(a.querySelector('blockquote'), null)
    assert.ok(b.querySelector('blockquote'))
    finishRead([first, second])
    await staleRead
    m.flushSync()
    assert.equal(a.querySelector('blockquote'), null)

    element<HTMLButtonElement>(b, '.admit').click()
    await Promise.resolve()
    await Promise.resolve()
    m.flushSync()
    assert.deepEqual(calls.at(-1), ['admit', 'b', 'repo#7', 'issue:19'])
    assert.equal(m.get(m.heldComments).comments.length, 0)
  } finally {
    for (const component of mounted) m.unmount(component)
    dom.window.close()
    await rm(dir, { recursive: true, force: true })
    for (const name of [...domNames, 'heldRead', 'heldAction']) Reflect.deleteProperty(globalThis, name)
  }
})
