import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile } from 'svelte/compiler'
import { build } from 'esbuild'
import type { ChangeDetail, Finding, FindingReply, ReviewRound } from '../api/change.ts'
import { need } from '../../../test/need.ts'
import { openFindings, roundReview, ownerReview, firstSentence, type ThreadEntry, type TimelineEntry } from './github.ts'

// Render the real Conversation on the server and read the text.
async function load() {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-conversation-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './Conversation.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onResolve({ filter: /ui[/\\]format\.ts$/ }, () => ({ path: 'format', namespace: 'stub' }))
      builder.onLoad({ filter: /.*/, namespace: 'stub' }, () => ({ contents: "export const ago = () => '1m ago'; export const markdown = (s) => s", loader: 'js' }))
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({ contents: 'export const api = {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, 'Conversation.mjs')
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return (props: object): string => module.render(module.default, { props }).body
}

const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')
const reply = (disposition: string): FindingReply => ({ disposition, message: '', head: '', author: '', at: '' })
const finding = (id: string, replies: FindingReply[] = [], severity = 'should-fix'): Finding => ({ id, severity, file: 'a.go', line: 7, why: `why ${id}`, fix: `fix ${id}`, replies })
const round = (n: number, extra: Partial<ReviewRound> = {}): ReviewRound => ({ round: n, head: 'h', verdict: 'changes', summary: '', findings: [], at: '2026-10-02T11:00:00Z', ...extra })
const detail = { author: { login: 'laptop-load' }, createdAt: '2026-10-02T10:00:00Z', body: 'Removes the old runner.' }
const post = (login: string, body: string, at: string): TimelineEntry => {
  const entry: ThreadEntry = { author: { login }, body, at }
  return { kind: 'post', at, post: entry }
}
const round1 = round(1, { summary: 'Good removal.', findings: [finding('f1', [reply('fixed')]), finding('f2'), finding('f3', [reply('question')])] })
const full = (rounds: ReviewRound[]): Partial<ChangeDetail> => ({ key: 'k', reviewerSession: 'rv', verdict: 'changes', reviewRound: rounds.length, reviewRounds: rounds, findings: rounds.at(-1)?.findings ?? [] })

test('openFindings leaves out findings the author fixed or the owner declined', () => {
  assert.deepEqual(openFindings(round1).map((f) => f.id), ['f2', 'f3'])
  assert.deepEqual(openFindings(null), [])
})

test('the conversation is a timeline with the review round inline and a pinned open-findings card', async () => {
  const render = await load()
  const events = [post('laptop-load', 'Fixed the hook import.', '2026-10-02T12:00:00Z'), { kind: 'round', at: round1.at, round: round1 }]
  const html = render({ detail, events, bots: [{ bot: 'coderabbitai', state: 'commented', findings: 0 }], change: full([round1]), names: { rv: 'sol' }, checks: [{ name: 'build', state: 'success' }, { name: 'test', state: 'success' }] })
  const t = text(html)
  assert.match(t, /Round 1 · 2 open/)
  assert.match(html, /class="pin"[^>]*>|class="pin /)
  assert.match(html, /id="latest-round"/)
  assert.match(t, /Show review/)
  assert.match(t, /should-fix · a\.go:7 — why f2/)
  assert.doesNotMatch(t, /fix f2/)
  assert.doesNotMatch(t, /Review rounds/i)
  assert.equal((t.match(/Changes requested/g) ?? []).length, 0)
  assert.match(t, /sol changes requested · round 1 · 3 findings/)
  assert.match(t, /opened this/)
  assert.match(t, /CI passed · 2 of 2 checks/)
  assert.match(t, /coderabbitai/)
  assert.ok(t.indexOf('Removes the old runner') < t.indexOf('Fixed the hook import'))
  assert.ok(t.indexOf('Fixed the hook import') < t.indexOf('coderabbitai'))
})

test('with no open findings the pinned card is gone', async () => {
  const render = await load()
  const done = { ...round1, verdict: 'approve', findings: [finding('f1', [reply('fixed')])] }
  const html = render({ detail, events: [{ kind: 'round', at: done.at, round: done }], bots: [], change: full([done]), names: {}, checks: [{ name: 'build', state: 'success' }, { name: 'test', state: 'success' }] })
  assert.doesNotMatch(html, /class="pin/)
  assert.doesNotMatch(text(html), /· \d+ open/)
})

test('a reviewed local change shows its round with no GitHub detail', async () => {
  const render = await load()
  const html = render({ detail: null, events: [{ kind: 'round', at: round1.at, round: round1 }], bots: [], change: full([round1]), names: {}, checks: [] })
  const t = text(html)
  assert.match(t, /round 1 · 3 findings/)
  assert.match(t, /Show review/)
  assert.match(t, /Round 1 · 2 open/)
})

test('the CI line does not call cancelled, action-required or unknown checks passed', async () => {
  const render = await load()
  for (const c of [{ state: 'cancelled' }, { conclusion: 'action_required' }, { conclusion: 'weird' }]) {
    const t = text(render({ detail, events: [], bots: [], change: null, names: {}, checks: [{ name: 'build', state: 'success' }, { name: 'x', ...c }] }))
    assert.match(t, /CI incomplete · 1 of 2 checks passed/)
    assert.doesNotMatch(t, /CI passed/)
  }
})

test('only the latest round offers replies; an earlier round with a dropped finding is read-only', () => {
  const old = { ...round1, findings: [finding('gone')] }
  const latest = round(2, { findings: [finding('now')] })
  const c = full([old, latest])
  assert.equal(roundReview(c, old, latest, 'r').roomId, '')
  assert.deepEqual((roundReview(c, old, latest, 'r').change.findings ?? []).map((f) => f.id), ['gone'])
  assert.equal(roundReview(c, latest, latest, 'r').roomId, 'r')
  assert.deepEqual(roundReview(c, latest, latest, 'r').change.reviewRounds, [])
})

test('the CI line names failed and running checks', async () => {
  const render = await load()
  const line = (checks: object[]) => text(render({ detail, events: [], bots: [], change: null, names: {}, checks }))
  assert.match(line([{ state: 'success' }, { state: 'failure' }]), /CI failed · 1 of 2 checks/)
  assert.match(line([{ state: 'success' }, { state: 'pending' }]), /CI running · 1 of 2 checks/)
})

test('firstSentence keeps the opening sentence of a long review', () => {
  assert.equal(firstSentence('Approved. The change is small.\n\nSecond paragraph.'), 'Approved.')
  assert.equal(firstSentence('**Looks good** to me\n\nMore.'), 'Looks good to me')
  assert.equal(firstSentence(''), '')
})

test('an approved round with no findings shows only the first sentence until opened', async () => {
  const render = await load()
  const done = round(1, { verdict: 'approve', summary: 'Approved. The removal is clean.\n\nSecond paragraph with detail.\n\nThird paragraph.' })
  const html = render({ detail, events: [{ kind: 'round', at: done.at, round: done }], bots: [], change: full([done]), names: {}, checks: [] })
  const t = text(html)
  assert.match(t, /Approved\./)
  assert.doesNotMatch(t, /Second paragraph/)
  assert.match(t, /Show review/)
  assert.match(html, /class="rcard[^"]* ok/)
})

test('ownerReview keeps only owner comments, with replies enabled, whatever the rounds hold', () => {
  const first = round(1, { findings: [finding('f1', [], 'blocking')] })
  const c: Partial<ChangeDetail> = { findings: [finding('f1', [], 'blocking'), finding('o1', [], 'owner')], reviewRounds: [first] }
  const r = ownerReview(c, 'room')
  assert.deepEqual((r.change.findings ?? []).map((f) => f.id), ['o1'])
  assert.equal(r.roomId, 'room')
  assert.deepEqual(r.change.reviewRounds, [])
  assert.deepEqual(ownerReview(null, 'room').change.findings, [])
  assert.deepEqual((roundReview(c, first, first, 'room').change.findings ?? []).map((f) => f.id), ['f1'])
})
