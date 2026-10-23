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
import type { Message } from '../api/room.ts'

// A delivered PR event in a session DM draws the same card as the feed: the
// facts, then a collapsed Details toggle that holds the text the agent read
// (system-events.md section 3.4.2).
const sessions = fileURLToPath(new URL('.', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-dm-event-'))
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

// The event and the text the agent read. The text carries a log line no card
// shows, so a test can tell the card from the raw input.
const RAW = 'CI failed (2/3) on PR #542 at head1: unit, build. Fix the failure and push to this PR.\n\nLog tail:\nboom: the parser test failed at line 42'
const post: Message = {
  id: 'msg_0123456789abcdef', ts: 1, roomId: 'room_1', from: 'room_1', to: ['room_1:author'], wake: true,
  kind: 'pr', notice: { kind: 'ci-failed' },
  payload: {
    type: 'ci-failed', label: 'PR #542', number: 542, location: 'https://github.com/acme/app/pull/542', changeId: 'github.com/acme/app#542', repo: 'acme/app',
    head: 'head1', url: 'https://github.com/acme/app/pull/542', ciAttempt: 2, ciMax: 3, summary: 'unit, build', detail: RAW,
  },
  text: RAW,
}

function view() {
  return web.dmView({ address: 'room_1:author', posts: [post], turns: [] }, { name: 'room_1' })
}

test('a PR event reaches a session DM as a system-event row', () => {
  const dm = view()
  const row = dm.rows.find((r: { kind: string }) => r.kind === 'system-event')
  assert.ok(row, `rows: ${dm.rows.map((r: { kind: string }) => r.kind).join()}`)
  assert.equal(row.post.id, post.id)
})

// A notice that opened a turn appears as a transcript prompt item, not as a
// post row. It still draws the card, not the raw text.
test('a PR event that woke the turn draws its card, not the prompt text', () => {
  const dm = web.dmView({
    address: 'room_1:author',
    posts: [post],
    turns: [{ n: 1, started: '2026-10-23T08:00:00Z', items: [{ kind: 'prompt', from: 'room_1', postId: post.id, delivered: true }] }],
  }, { name: 'room_1' })
  const rows = dm.rows.filter((r: { kind: string }) => r.kind === 'system-event' || r.kind === 'message')
  assert.equal(rows.length, 1, `rows: ${rows.map((r: { kind: string }) => r.kind).join()}`)
  assert.equal(rows[0].kind, 'system-event')
})

test('the DM shows the PR card and hides the agent text until Details opens', () => {
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.DmRows, {
    target,
    props: {
      dm: view(), room: { id: 'room_1', name: 'room_1' }, member: { name: 'author', address: 'room_1:author', hue: 0 },
      onthread: () => {}, onopenfile: () => {},
    },
  })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  assert.match(text, /PR #542/)
  assert.match(text, /CI failed 2\/3/)
  assert.doesNotMatch(text, /boom: the parser test failed/)
  const toggle = need(target.querySelector('button[aria-expanded]'), 'the Details toggle')
  assert.ok(toggle instanceof HTMLButtonElement, 'the Details toggle is a button')
  toggle.click()
  web.flushSync()
  assert.match(target.textContent ?? '', /boom: the parser test failed/)
  web.unmount(mounted)
  target.remove()
})
