import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { installDom, domNames } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'
import type { SessionReference } from '../api/session.ts'
import type { PREvent } from '../rooms/events.gen.ts'

// A post of another conversation reaches a session's DM as one compact row that
// names the post and opens it where it lives, never as a second copy.
const sessions = fileURLToPath(new URL('.', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-dm-reference-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})

const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  loader: { '.svg': 'dataurl' },
  stdin: {
    contents: `export { default as DmRows } from './DmRows.svelte';
      export { dmView } from './dm.ts';
      export { mount, unmount, flushSync } from 'svelte';`,
    resolveDir: sessions,
    loader: 'js',
  },
  plugins: [svelteEsbuildPlugin()],
})
const app = join(dir, 'dm.mjs')
await writeFile(app, need(built.outputFiles?.[0]).contents)
const web = await import(pathToFileURL(app).href)

const reference: SessionReference = {
  postId: 'msg_0123456789abcdef', from: 'room_1:lead', speaker: 'lead', fromHue: 2,
  conversation: 'room', ts: Date.UTC(2026, 9, 23, 8, 0), preview: 'The parser test failed on the new input.', attachments: 2,
}
const members = [{ id: 'ses_lead', name: 'lead', address: 'room_1:lead', hue: 2 }]

function view(refs: SessionReference[]) {
  return web.dmView({ address: 'room_1:author', posts: [], references: refs, turns: [] }, { name: 'room_1' })
}

test('a reference row shows its sender, its home, a one-line preview and its files', () => {
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.DmRows, {
    target,
    props: {
      dm: view([reference]), room: { id: 'room_1', name: 'room_1' }, member: { name: 'author', address: 'room_1:author', hue: 0 },
      members, onthread: () => {}, onopenfile: () => {}, onpost: () => {},
    },
  })
  web.flushSync()
  const text = (target.textContent ?? '').replace(/\s+/g, ' ').trim()
  assert.match(text, /lead/)
  assert.match(text, /from Room chat/)
  assert.match(text, /The parser test failed on the new input\./)
  assert.match(text, /2 files/)
  assert.match(text, /Jump to message/)
  web.unmount(mounted)
  target.remove()
})

test('a typed event reference uses its card facts and keeps its jump action', () => {
  const payload = {
    type: 'ready', label: 'PR #379', location: 'https://github.com/resident-labs/dyna/pull/379', changeId: 'github.com/resident-labs/dyna#379',
    number: 379, repo: 'resident-labs/dyna', title: 'room: clean orphaned post index rows at startup', head: 'b2fb3534', url: 'https://github.com/resident-labs/dyna/pull/379',
  } satisfies PREvent
  const typed = Object.assign({}, reference, { from: 'dyna', speaker: 'dyna', kind: 'pr', notice: { kind: 'ready' }, payload, preview: 'raw engine fallback' })
  const target = document.createElement('div')
  document.body.append(target)
  const jumped: string[] = []
  const mounted = web.mount(web.DmRows, {
    target,
    props: {
      dm: view([typed]), room: { id: 'room_1', name: 'room_1' }, member: { name: 'author', address: 'room_1:author', hue: 0 },
      members, onthread: () => {}, onopenfile: () => {}, onpost: (postId: string) => jumped.push(postId),
    },
  })
  web.flushSync()
  const text = (target.textContent ?? '').replace(/\s+/g, ' ').trim()
  assert.match(text, /dyna/)
  assert.match(text, /PR #379/)
  assert.match(text, /Ready to land/)
  assert.match(text, /room: clean orphaned post index rows at startup/)
  assert.doesNotMatch(text, /raw engine fallback/)
  const jump = [...target.querySelectorAll('button')].find((button) => button.textContent?.includes('Jump to message'))
  assert.ok(jump, 'the typed reference keeps its jump action')
  jump.click()
  web.flushSync()
  assert.deepEqual(jumped, [reference.postId])
  web.unmount(mounted)
  target.remove()
})

test('clicking a reference row opens its post where it was written', () => {
  const target = document.createElement('div')
  document.body.append(target)
  const jumped: string[] = []
  const mounted = web.mount(web.DmRows, {
    target,
    props: {
      dm: view([reference]), room: { id: 'room_1', name: 'room_1' }, member: { name: 'author', address: 'room_1:author', hue: 0 },
      members, onthread: () => {}, onopenfile: () => {}, onpost: (postId: string) => jumped.push(postId),
    },
  })
  web.flushSync()
  const open = need(target.querySelector('button'), 'the reference row button')
  assert.ok(open instanceof HTMLButtonElement, 'the row is one button')
  open.click()
  web.flushSync()
  assert.deepEqual(jumped, [reference.postId])
  web.unmount(mounted)
  target.remove()
})
