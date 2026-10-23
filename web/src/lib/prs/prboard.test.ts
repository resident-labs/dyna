import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile, compileModule } from 'svelte/compiler'
import { build } from 'esbuild'
import { mergedToday, neighbor } from './prboard.ts'
import { GROUPS, laneOf } from './prlist.ts'
import { timeline, type ThreadEntry } from './github.ts'
import type { ReviewRound } from '../api/change.ts'
import type { Row } from './prlist.ts'
import { need } from '../../../test/need.ts'

const open = (o: Row = {}): Row => ({ changeId: 'c', kind: 'pr', number: 1, state: 'open', status: 'ci-running', ci: 'pending', ...o })
const ready = (o: Row = {}): Row => open({ status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', ...o })
const reviewing = (o: Row = {}): Row => open({ ci: 'passed', reviewState: 'reviewing', reviewRound: 3, ...o })

test('a change sits in the lane for where it stands', () => {
  assert.equal(laneOf(ready()), 'landing')
  assert.equal(laneOf(open({ verdict: 'changes', ci: 'passed', reviewState: 'changes' })), 'fix')
  assert.equal(laneOf(open({ ci: 'pending' })), 'ci')
  assert.equal(laneOf(open({ ci: 'failed', status: 'ci-failed' })), 'fix')
  assert.equal(laneOf(reviewing()), 'review')
  assert.equal(laneOf(open({ ci: 'passed', reviewState: 'queued' })), 'review')
  assert.equal(laneOf(open({ state: 'merged' })), 'done')
  assert.equal(laneOf(open({ status: 'untracked' })), null)
})

test('merged today means seen since local midnight', () => {
  const now = new Date(2026, 9, 18, 15, 0)
  const at = (h: number, d = 18) => new Date(2026, 9, d, h).toISOString()
  const rows = [
    open({ changeId: 'a', state: 'merged', checked: at(9) }),
    open({ changeId: 'b', state: 'merged', checked: at(23, 17) }),
    open({ changeId: 'c', state: 'merged', checked: at(1, 16) }),
    open({ changeId: 'd', state: 'open', checked: at(9) }),
    open({ changeId: 'e', state: 'merged' }),
  ]
  assert.deepEqual(mergedToday(rows, now).map((r) => r.changeId), ['a'])
  assert.deepEqual(mergedToday(null, now), [])
})

test('j and k stop at the first and last row', () => {
  const rows = [{ changeId: 'a' }, { changeId: 'b' }, { changeId: 'c' }]
  assert.equal(neighbor(rows, 'a', 1)?.changeId, 'b')
  assert.equal(neighbor(rows, 'b', -1)?.changeId, 'a')
  assert.equal(neighbor(rows, 'c', 1)?.changeId, 'c')
  assert.equal(neighbor(rows, 'a', -1)?.changeId, 'a')
  assert.equal(neighbor(rows, '', 1)?.changeId, 'a') // no open row yet: start at the first
  assert.equal(neighbor([], 'a', 1), null)
})

test('the timeline joins comments and reviewer rounds in time order', () => {
  const post = (at: string, body: string): ThreadEntry => ({ author: { login: 'a' }, at, body })
  const thread = [post('2026-10-18T10:00:00Z', 'a'), post('2026-10-18T12:00:00Z', 'c')]
  const rounds: ReviewRound[] = [{ round: 1, head: 'h', verdict: 'approve', summary: '', findings: [], at: '2026-10-18T11:00:00Z' }]
  const out = timeline(thread, rounds)
  assert.deepEqual(out.map((e) => e.kind), ['post', 'round', 'post'])
  const second = need(out[1])
  assert.ok(second.kind === 'round')
  assert.equal(second.round.round, 1)
  assert.deepEqual(timeline(null, undefined), [])
})

// The components, rendered on the server.
async function load(name: string) {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-pr-board-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './${name}.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte\.js$/ }, ({ path }) => ({ contents: compileModule(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]format\.ts$/ }, () => ({ contents: "export const markdown = (t) => t || ''; export const ago = () => 'now'; export const initials = (n) => n.slice(0, 2); export const short = (n) => n;", loader: 'js' }))
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({ contents: 'export const api = {}; export const copyText = async () => {}; export const openExternal = () => {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, `${name}.mjs`)
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render }
}
const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')

test('the session card keeps the status track and its actions inline', async () => {
  const { dir, component, render } = await load('PRStory')
  try {
    const card = render(component, { props: { pr: reviewing({ title: 'Require --goal', url: 'u' }), roomId: 'r' } }).body
    assert.match(card, /aria-label="Progress"/)
    assert.doesNotMatch(card, /pinned/)
    assert.doesNotMatch(text(card), /Merge pull request/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('a row without a merge item keeps its review lane while CI runs', () => {
  assert.equal(laneOf(open({ reviewState: 'reviewing' })), 'review')
  assert.equal(laneOf(open({ reviewState: 'queued' })), 'review')
  assert.equal(laneOf(open({ verdict: 'approve', reviewState: 'approved' })), 'ci')
  assert.equal(laneOf(open({ ci: 'failed', reviewState: 'reviewing' })), 'fix')
})

test('the list groups are Blocked, In progress, then the folds', () => {
  assert.deepEqual(GROUPS.map((g) => [g.id, g.label]), [['blocked', 'Blocked'], ['progress', 'In progress'], ['done', 'Merged today'], ['untracked', 'Not tracked']])
  assert.equal(need(GROUPS.find((g) => g.id === 'done')).collapsed, true)
})

test('the queue lists the merge order, one line per PR with a status dot', async () => {
  const { dir, component, render } = await load('PRList')
  try {
    const row = (n: number, t: string) => ready({ changeId: `c${n}`, number: n, title: t, checks: [{ name: 'a', state: 'success' }, { name: 'b', state: 'success' }] })
    const queue = [{ row: row(7, 'First in line'), item: { state: 'ready' }, state: { label: 'Merging next', tone: 'sage', lane: 'queue' } },
      { row: row(8, 'Second in line'), item: { state: 'ready' }, state: { label: 'Ready · 2nd', tone: 'sage', lane: 'queue' } }]
    const blocked = open({ changeId: 'x', number: 9, title: 'Broken one', checks: [{ name: 'a', state: 'failure' }] })
    const groups = [{ id: 'blocked', label: 'Blocked', foldable: true, rows: [blocked] }]
    const states = { x: { label: 'CI failed', tone: 'coral', reason: 'CI failed: test', lane: 'blocked' } }
    const html = render(component, { props: { queue, groups, states } }).body
    assert.doesNotMatch(html, /<section class="card[ "]/)
    assert.match(text(html), /Merge queue · 2 First in line #7 Second in line #8 .*Blocked · 1 Broken one #9 0\/1 checks/)
    assert.doesNotMatch(text(html), /Merging next|Ready · 2nd|2\/2 checks/)
    assert.match(html, /class="sdot sage[^"]*"[^>]*title="Merging next"/)
    assert.match(html, /class="sdot coral[^"]*"[^>]*title="CI failed · CI failed: test"/)
    // No queue while nothing is queued.
    assert.doesNotMatch(render(component, { props: { queue: [], groups, states } }).body, /Merge queue/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('a PR link opens that PR on its tab beside the list', async () => {
  const { dir, component, render } = await load('GitHub')
  try {
    const rows = [ready({ changeId: 'a', number: 7, title: 'First' }), reviewing({ changeId: 'b', number: 8, title: 'Linked example' })]
    const html = render(component, { props: { roomId: 'r', changes: rows, link: { pr: '8', tab: 'checks' } } }).body
    assert.match(html, /<h1[^>]*>Linked example <span[^>]*>#8<\/span><\/h1>/)
    assert.match(html, /role="tab" aria-selected="true"[^>]*>Checks/)
    assert.match(text(html), /Conversation/)
    assert.match(html, /<button class="row[^"]*sel[^"]*"[^>]*aria-pressed="true"[^>]*>.*?Linked example/s)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('the PR page has a state chip under the title and a merge box after the conversation', async () => {
  const { dir, component, render } = await load('PRDetail')
  try {
    const change = open({ changeId: 'c', key: 'c', number: 772, title: 'Use event waits', ci: 'pending', verdict: 'approve', reviewState: 'approved', reviewRound: 1,
      checks: [{ name: 'go-test', state: 'pending' }, { name: 'frontend', state: 'success' }] })
    const state = { id: 'ci', lane: 'progress', label: 'Waiting for CI', tone: 'run', reason: '' }
    const html = render(component, { props: { change, roomId: 'r', merge: { state: 'ci' }, standing: state, detail: { author: { login: 'a' }, createdAt: '2026-10-19T00:00:00Z', body: 'Body' } } }).body
    const t = text(html)
    assert.match(t, /Use event waits #772/)
    assert.match(t, /Open .*Waiting for CI/)
    assert.match(html, /class="mbox[ "]/)
    assert.match(t, /Some checks are still running 1 of 2 checks passed · go-test running/)
    assert.match(t, /Approved by the reviewer/)
    assert.ok(t.indexOf('Body') < t.indexOf('Some checks are still running'), 'the merge box follows the conversation')
    assert.doesNotMatch(html, /class="nums"/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})


test('the merge queue says a shared wait once, in its header', async () => {
  const { dir, component, render } = await load('PRList')
  try {
    const reason = 'Automatic merging is waiting for completed GitHub CI on main at c72fff01234567890123456789012345678901234'
    const queue = [1, 2, 3].map((n) => ({
      row: ready({ changeId: `c${n}`, number: n, title: `Change ${n}`, checks: [{ name: 'a', state: 'success' }] }),
      item: { main: { state: 'pending', head: 'c72fff01234567890123456789012345678901234', reason } },
      state: { label: 'Waiting for main CI', tone: 'gold', reason: n === 3 ? 'Waiting for a required check' : reason },
    }))
    const html = render(component, { props: { queue, groups: [], states: {} } }).body
    assert.match(text(html), /^ ?▾ Merge queue · 3 · waiting for main CI Change 1 #1 Change 2 #2 Change 3 #3 ?$/)
    // A row's own reason stays on its dot.
    assert.match(html, /title="Waiting for main CI · Waiting for a required check"/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})
