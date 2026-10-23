import test, { after } from 'node:test'
import { icons } from './icons.ts'
import type { CallState } from './callRequests.ts'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { callsInWindow, feedRows } from '../rooms/feedRows.ts'

const dir = new URL('.', import.meta.url).pathname
async function load(name: string) {
  const tmp = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-call-ui-'))
  after(() => rm(tmp, { recursive: true, force: true }))
  const result = await build({
    bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    stdin: { contents: `export { default } from './${name}.svelte'; export { render } from 'svelte/server'`, resolveDir: dir, loader: 'js' },
    plugins: [svelteEsbuildPlugin('server')],
  })
  const file = join(tmp, `${name}.mjs`)
  await writeFile(file, result.outputFiles[0]?.contents ?? '')
  return import(pathToFileURL(file).href)
}
const rowModule = await load('CallRequestRow')
const toastModule = await load('IncomingToast')

const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
const request = (state: CallState) => ({ id: 'c7', room: 'room_1', reason: 'CI on main is red', state, askedAt: '2026-10-20T10:00:00Z' })

test('the room row names the reason and the state, and offers Accept and Decline while it rings', () => {
  const { default: Row, render } = rowModule
  const show = (state: CallState) => text(render(Row, { props: { request: request(state), onaccept() {}, ondecline() {} } }).body)
  const html = render(Row, { props: { request: request('ringing'), onaccept() {}, ondecline() {} } }).body
  assert.ok(html.includes(`d="${icons.phone}"`), 'the row uses the shared phone stroke icon')
  assert.match(html, /<q[^>]*>CI on main is red<\/q>/)
  assert.doesNotMatch(html, /☎/)
  assert.equal(show('ringing'), 'root asked to call you: CI on main is red · ringing Accept Decline')
  for (const state of ['joined', 'declined', 'withdrawn'] as const) assert.equal(show(state), `root asked to call you: CI on main is red · ${state}`)
})

test('the incoming toast shows the reason, and covers the screen after a push tap', () => {
  const { default: Toast, render } = toastModule
  const html = (full: boolean) => render(Toast, { props: { request: request('ringing'), full, onaccept() {}, ondecline() {} } }).body
  assert.match(html(false), /aria-label="root would like to speak with you"/)
  assert.match(text(html(false)), /root would like to speak with you CI on main is red Accept Decline$/)
  assert.doesNotMatch(html(false), /class="[^"]*\bfull\b/)
  assert.match(html(true), /class="[^"]*\bfull\b/)
})

test('a request is a feed row at the time root asked, between the posts', () => {
  const post = (id: string, iso: string) => ({ id, ts: Date.parse(iso), from: 'a', to: ['b'], text: id })
  const posts = [post('p1', '2026-10-20T09:00:00Z'), post('p2', '2026-10-20T11:00:00Z')]
  const kinds = (rows: { kind: string; key: string }[]) => rows.filter((r) => r.kind !== 'day').map((r) => r.key)
  assert.deepEqual(kinds(feedRows(posts, '', [request('ringing')])), ['p1', 'call:room_1/c7', 'p2'])
  assert.deepEqual(kinds(feedRows(posts, '', [{ ...request('ringing'), askedAt: '2026-10-20T12:00:00Z' }])), ['p1', 'p2', 'call:room_1/c7'])
  assert.deepEqual(kinds(feedRows([], '', [request('joined')])), ['call:room_1/c7'])
})

test('a window that stops short of older or newer posts shows no request from beyond its edge', () => {
  const posts = [{ ts: Date.parse('2026-10-20T10:30:00Z') }, { ts: Date.parse('2026-10-20T10:40:00Z') }]
  const calls = [request('ringing')]
  assert.equal(callsInWindow(calls, posts, 0, 0).length, 1)
  assert.equal(callsInWindow(calls, posts, 5, 0).length, 0, 'older posts are not loaded')
  assert.equal(callsInWindow(calls, posts, 0, 5).length, 1)
  assert.equal(callsInWindow([{ ...request('ringing'), askedAt: '2026-10-20T11:00:00Z' }], posts, 0, 5).length, 0, 'newer posts are not loaded')
})
