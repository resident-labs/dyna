import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile } from 'svelte/compiler'
import { build } from 'esbuild'
import type { BotReview } from '../api/change.ts'
import { classify, listView, mergeState, mergeBox, reviewerRef, rowsFromGitHub, botNote, statusBox, factsLine, isReady, isBotAuthor, asksChanges, botKey, moveKey, type MergeItem, type Row } from './prlist.ts'
import type { CheckRow } from './pr.ts'

const open = (o: Row = {}): Row => ({ changeId: 'c', kind: 'pr', number: 1, state: 'open', status: 'ci-running', ci: 'pending', ...o })
const ready = (o: Row = {}): Row => open({ status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', ...o })

// need returns the value, or fails the test when a row is missing.
function need<T>(value: T | null | undefined): T {
  assert.ok(value !== null && value !== undefined)
  return value
}
const chk = (...states: string[]): CheckRow[] => states.map((state, i) => ({ name: `c${i}`, state }))
const bot = (name: string, state: string): BotReview => ({ bot: name, state, findings: 0, url: '' })

test('one chip per row shows the agent state', () => {
  assert.deepEqual(classify(ready()), { group: 'landing', label: 'Ready to land', tone: 'sage' })
  assert.deepEqual(classify(ready({ verdict: 'approve-with-nits' })).label, 'Ready to land')
  assert.deepEqual(classify(open({ status: 'changes-requested', ci: 'passed', verdict: 'changes', reviewState: 'changes' })), { group: 'fix', label: 'Changes asked', tone: 'coral' })
  assert.equal(classify(open({ ci: 'passed', verdict: 'question' })).label, 'Question')
  assert.deepEqual(classify(open()), { group: 'ci', label: 'CI running', tone: 'gold' })
  assert.deepEqual(classify(open({ ci: 'passed', reviewState: 'reviewing' })), { group: 'review', label: 'Reviewing', tone: 'iris' })
  assert.equal(classify(open({ ci: 'passed', reviewState: 'queued', queuePosition: 2 })).label, 'Queued (2nd)')
  assert.equal(classify(open({ ci: 'failed', status: 'ci-failed', consecutiveFailures: 2 })).label, 'CI failed 2/3')
  assert.deepEqual(classify(open({ status: 'paused', ciPaused: true, pauseReason: 'x' })), { group: 'ci', label: 'Paused', tone: 'gold' })
  assert.equal(classify(open({ status: 'untracked' })).group, 'untracked')
  assert.equal(classify(open({ state: 'merged' })).label, 'Merged')
  assert.equal(classify(open({ state: 'closed', status: 'untracked' })).label, 'Closed')
  // Approval still needs passing GitHub CI before merge readiness.
  assert.equal(classify(ready({ ci: 'absent' })).label, 'Approved')
  // Local changes have no CI.
  assert.equal(classify({ changeId: 'l', kind: 'local', state: 'open', verdict: 'approve', reviewState: 'approved' }).label, 'Approved')
})

test('one merge state per row, from the merge service item', () => {
  const st = (c: Row, item: MergeItem | null, pos?: number) => { const m = mergeState(c, item, pos); return [m.id, m.lane, m.label, m.tone] }
  assert.deepEqual(st(ready(), { state: 'ready' }, 1), ['next', 'queue', 'Next batch', 'sage'])
  assert.deepEqual(st(ready(), { state: 'ready' }, 2), ['ready', 'queue', 'Ready · 2nd', 'sage'])
  assert.deepEqual(st(ready(), { state: 'trial-running' }, 1), ['trial-running', 'queue', 'Testing batch', 'gold'])
  assert.deepEqual(st(ready(), { state: 'trial-failed' }, 1), ['trial-failed', 'queue', 'Batch CI failed', 'coral'])
  assert.deepEqual(st(open(), { state: 'ci-failed', reason: 'CI failed: test' }), ['ci-failed', 'blocked', 'CI failed', 'coral'])
  assert.equal(mergeState(open(), { state: 'ci-failed', reason: 'CI failed: test' }).reason, 'CI failed: test')
  // A reason that only repeats the chip is dropped; others start with a capital.
  assert.equal(mergeState(open(), { state: 'changes', reason: 'changes requested' }).reason, '')
  assert.equal(mergeState(open(), { state: 'conflict', reason: 'conflicts with main; rebase' }).reason, 'Conflicts with main; rebase')
  assert.deepEqual(st(open(), { state: 'changes', reason: 'changes requested' }), ['changes', 'blocked', 'Changes requested', 'coral'])
  assert.deepEqual(st(open(), { state: 'changes', reason: 'question for the author' }), ['changes', 'blocked', 'Question', 'gold'])
  assert.deepEqual(st(open(), { state: 'conflict' }), ['conflict', 'blocked', 'Conflict', 'coral'])
  assert.deepEqual(st(open({ isDraft: true }), { state: 'held' }), ['held', 'blocked', 'Draft', 'faint'])
  assert.deepEqual(st(open(), { state: 'paused' }), ['paused', 'blocked', 'Paused', 'gold'])
  assert.deepEqual(st(open(), { state: 'review' }), ['review', 'progress', 'Needs review', 'quiet'])
  assert.deepEqual(st(open({ reviewState: 'reviewing' }), { state: 'review' }), ['reviewing', 'progress', 'Reviewing', 'iris'])
  assert.deepEqual(st(open(), { state: 'ci' }), ['ci', 'progress', 'Waiting for CI', 'run'])
  assert.deepEqual(st(open(), { state: 'waiting-base' }), ['waiting-base', 'progress', 'Waiting for the base PR', 'gold'])
  assert.deepEqual(mergeState(open(), { state: 'waiting-dependency', dependency: { changeId: 'dependency-change', number: 2, title: 'Prepare the base' } }), { id: 'waiting-dependency', lane: 'progress', label: 'Waiting for dependency', tone: 'gold', reason: '#2 · Prepare the base' })
  assert.equal(mergeState(open(), { state: 'waiting-dependency', dependency: { changeId: 'change_local', title: 'Prepare the local base' } }).reason, 'Prepare the local base')
  assert.equal(mergeState(open(), { state: 'waiting-dependency', dependency: { changeId: 'change_missing' } }).reason, 'Unresolved change')
  assert.deepEqual(st(open({ state: 'merged' }), null), ['merged', 'done', 'Merged', 'done'])
  // A ready PR that the merge service has not listed does not claim a queue place.
  assert.deepEqual(st(ready(), null), ['approved', 'progress', 'Approved', 'sage'])
  assert.equal(mergeState(ready(), null).reason, 'Waiting for the merge service')
  // A row the merge service does not list keeps its review chip.
  assert.deepEqual(st({ changeId: 'l', kind: 'local', state: 'open', verdict: 'approve', reviewState: 'approved' }, null), ['local', 'progress', 'Approved', 'run'])
  assert.deepEqual(st(open({ status: 'untracked' }), null).slice(0, 2), ['untracked', 'untracked'])
})

test('the list puts queued PRs on the queue card in merge order, and groups the rest', () => {
  const rows = [
    open({ changeId: 'a', number: 10 }),
    ready({ changeId: 'b', number: 11 }),
    ready({ changeId: 'c', number: 12 }),
    open({ changeId: 'd', number: 13, verdict: 'changes', ci: 'passed' }),
    open({ changeId: 'e', number: 14, status: 'untracked' }),
    open({ changeId: 'f', number: 15, state: 'merged' }),
  ]
  const merge = [{ changeId: 'c', state: 'ready' }, { changeId: 'b', state: 'ready' }, { changeId: 'a', state: 'ci' }, { changeId: 'd', state: 'changes', reason: 'changes requested' }]
  const { queue, groups } = listView(rows, merge)
  assert.deepEqual(queue.map((q) => [q.row.changeId, q.state.label]), [['c', 'Next batch'], ['b', 'Ready · 2nd']])
  assert.deepEqual(groups.map((g) => [g.id, g.rows.map((r) => r.changeId)]), [['blocked', ['d']], ['progress', ['a']], ['done', ['f']], ['untracked', ['e']]])
  assert.equal(need(groups.at(-1)).fold, true)
  assert.equal(need(groups.find((g) => g.id === 'done')).collapsed, true)
  // Before the merge status loads, every PR row keeps its own chip and no queue shows.
  assert.deepEqual(listView(rows, null).queue, [])
  assert.deepEqual(listView([], []).groups, [])
})

test('GitHub PRs that no change covers become untracked rows', () => {
  const items = [{ number: 1, title: 'covered' }, { number: 2, title: 'plain', state: 'OPEN', author: { login: 'sam' }, headRefName: 'f', baseRefName: 'main', url: 'u' }, { number: 3, title: 'old', state: 'MERGED' }, { number: 4, state: 'CLOSED', author: { login: 'bot', isBot: true } }]
  const out = rowsFromGitHub(items, [open({ number: 1 })], 'o/r')
  assert.deepEqual(out.map((r) => [r.number, r.state, r.status, r.changeId]), [[2, 'open', 'untracked', 'gh:2'], [3, 'merged', 'untracked', 'gh:3'], [4, 'closed', 'untracked', 'gh:4']])
  assert.equal(need(out[0]).repo, 'o/r')
  assert.equal(classify(need(out[0])).group, 'untracked')
  assert.equal(classify(need(out[1])).label, 'Merged')
  assert.deepEqual(rowsFromGitHub(undefined, null), [])
  // A draft PR that Dyna does not track still shows as a draft.
  assert.equal(need(rowsFromGitHub([{ number: 5, state: 'OPEN', isDraft: true }], [])[0]).isDraft, true)
  assert.equal(need(rowsFromGitHub([{ number: 6, state: 'OPEN' }], [])[0]).isDraft, false)
})

test('a bot that asked for changes gets one note; bots never block', () => {
  assert.equal(botNote({}), '')
  assert.equal(botNote({ botReviews: [bot('coderabbit', 'commented')] }), '')
  assert.equal(botNote({ botReviews: [bot('coderabbit', 'changes_requested')] }), 'coderabbit asked for changes. This does not block the merge.')
  assert.equal(isBotAuthor({ login: 'coderabbitai' }), true)
  assert.equal(isBotAuthor({ login: 'dependabot[bot]' }), true)
  assert.equal(isBotAuthor({ login: 'sam', isBot: true }), true)
  assert.equal(isBotAuthor({ login: 'sam' }), false)
  assert.equal(isBotAuthor(null), false)
})

test('status box: steps, one sentence and one main button', () => {
  const wait = statusBox(open({ ci: 'pending' }))
  assert.deepEqual(wait.steps.map((s) => [s.name, s.state]), [['CI', 'run'], ['Review', 'wait'], ['Merge', 'wait']])
  assert.equal(wait.say, 'CI is running. You do not need to do anything yet.')
  const done = statusBox(ready(), { review: true })
  assert.deepEqual(done.steps.map((s) => s.state), ['ok', 'ok', 'run'])
  assert.equal(done.say, 'The reviewer approved this PR. It merges cleanly into main.')
  assert.equal(done.button, null)
  assert.equal(done.review, false)
  assert.equal(statusBox(ready({ hasReviewSummary: true })).review, true)
  assert.match(statusBox(ready(), { merge: { state: 'conflict' } }).say, /conflicts with main\. The author must rebase it/)
  assert.equal(need(statusBox(ready(), { merge: { state: 'conflict' } }).steps[2]).state, 'bad')
  assert.equal(statusBox(ready(), { merge: { state: 'conflict' } }).button, null)
  assert.equal(statusBox(ready({ baseRef: 'dev' })).say, 'The reviewer approved this PR. It merges cleanly into dev.')
})

test('status box: CI step says why CI is absent', () => {
  const box = statusBox(open({ ci: 'absent', ciReason: 'No CI ran for this change within 5 minutes.' }))
  assert.deepEqual(need(box.steps[0]), { name: 'CI', state: 'wait', note: 'No CI ran for this change within 5 minutes.' })
  assert.equal(need(statusBox(open({ ci: 'absent' })).steps[0]).note, 'no GitHub CI')
  assert.match(box.say, /Source review can continue, but merging requires passing GitHub CI/)
})

test('status box: pending CI step names the check that blocks readiness', () => {
  const reason = 'pi: hosted check cancelled, no local check'
  const box = statusBox(open({ ci: 'pending', ciReason: reason }))
  assert.deepEqual(need(box.steps[0]), { name: 'CI', state: 'run', note: reason })
})

test('agent states never ask the human to merge', () => {
  // Agents land changes after review and CI.
  for (const c of [open({ ci: 'pending' }), open({ ci: 'passed', reviewState: 'queued' }), open({ ci: 'passed', reviewState: 'reviewing' }), open({ ci: 'passed', verdict: 'changes' }), open({ ci: 'failed', status: 'ci-failed' }), open({ ci: 'passed', reviewState: 'off' })]) {
    assert.equal(statusBox(c).button, null, JSON.stringify(c))
  }
  assert.equal(isReady(open({ ci: 'passed' })), false)
})

test('review off never shows for closed or untracked PRs', () => {
  const off = statusBox(open({ ci: 'passed', reviewState: 'off' }))
  assert.match(off.say, /review is off, so nothing starts/)
  for (const c of [open({ state: 'closed', reviewState: 'off' }), open({ status: 'untracked', reviewState: 'off' }), open({ state: 'merged', reviewState: 'off' })]) {
    assert.doesNotMatch(statusBox(c, { review: false }).say, /off/i)
  }
  const none = statusBox(open({ status: 'untracked' }))
  assert.equal(none.say, 'Dyna does not watch this PR, so it gets no automatic review.')
  assert.deepEqual(none.button, { id: 'track', label: 'Track and review', primary: true })
  assert.deepEqual(none.steps, [])
  assert.equal(statusBox(open({ state: 'closed' })).button, null)
  assert.equal(statusBox(open({ state: 'merged' })).say, 'This PR is merged.')
  assert.match(statusBox({ kind: 'local', state: 'open', status: 'untracked' }).say, /this change/)
})

test('status box: pause, changes, bots and the reviewer session', () => {
  const paused = statusBox(open({ ciPaused: true, status: 'paused', pauseReason: 'PR has 3 consecutive failures' }))
  assert.match(paused.say, /failed three times/)
  assert.equal(need(paused.button).id, 'resume')
  assert.equal(statusBox(open({ ci: 'passed', verdict: 'changes' })).review, true)
  assert.equal(statusBox(open({ ci: 'passed', reviewState: 'reviewing' })).session, true)
  assert.equal(statusBox(open({ ci: 'passed', reviewState: 'queued', queuePosition: 1 })).say, 'CI passed. This PR is 1st in line for review.')
  assert.equal(statusBox(ready({ botReviews: [bot('rabbit', 'changes_requested')] })).note, 'rabbit asked for changes. This does not block the merge.')
  assert.equal(statusBox(ready({ botReviews: [bot('rabbit', 'approved')] })).note, '')
})

test('facts line is one line', () => {
  const c = open({ branch: 'feat/x', base: 'main', checked: 't' })
  assert.equal(factsLine({ ...c, authorSession: 's' }, { names: { s: 'lets-work' }, size: { files: 12, additions: 480, deletions: 36 }, ago: () => 'just now' }), 'lets-work · feat/x → main · 12 files +480 −36 · updated just now')
  assert.equal(factsLine({ kind: 'local', branch: 'b' }), 'Local change · b')
  assert.equal(factsLine(open({ author: 'sam' })), 'sam')
})

// The components, rendered on the server.
async function load(name: string) {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-pr-list-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './${name}.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]format\.ts$/ }, () => ({ contents: "export const markdown = (t) => t || ''; export const ago = () => 'now'; export const durationMs = () => '';", loader: 'js' }))
      builder.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}; export const copyText = async () => {}; export const openExternal = () => {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, `${name}.mjs`)
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render }
}
const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')

test('checks show a summary, the failure with its log first, and fold the rest', async () => {
  const { dir, component, render } = await load('PRChecks')
  try {
    const checks = [
      { name: 'CI / go-test', state: 'success', url: 'u' }, { name: 'CI / frontend', state: 'failure', url: 'https://x/run' },
      { name: 'CI / relay', state: 'skipped' }, { name: 'Lint / web', state: 'pending' },
    ]
    const bots = [{ bot: 'coderabbitai', state: 'changes_requested', findings: 3, url: 'https://x/1' }]
    const html = render(component, { props: { checks, bots, pr: { ci: 'failed', ciTestReportNote: 'JUnit report missing for web' } } }).body
    const t = text(html)
    assert.match(t, /1 failed 1 running · 1 passed · 1 skipped/)
    assert.match(t, /CI \/ frontend failed/)
    assert.ok(t.indexOf('CI / frontend failed') < t.indexOf('go-test'), 'the failure comes before the passed checks')
    // The report warning is about the whole change: it sits after the failures, never inside one.
    assert.match(t, /JUnit report missing for web/)
    assert.doesNotMatch(need(html.match(/<section class="fail[\s\S]*?<\/section>/))[0], /JUnit/)
    assert.match(html, /aria-label="Open the full log: CI \/ frontend"/)
    // Every check stays reachable inside a fold, grouped by workflow.
    assert.match(t, /1 passed · go-test CI go-test passed/)
    assert.match(t, /Lint web running/)
    assert.match(t, /1 skipped · relay/)
    assert.match(t, /Bot reviews · coderabbitai, do not block coderabbitai asked for changes 3 findings/)
    assert.match(t, /Bots never block a merge/)
    assert.doesNotMatch(text(render(component, { props: { checks, bots: [], pr: {} } }).body), /Bot reviews/)
    assert.match(text(render(component, { props: { checks: [], pr: { ci: 'absent' } } }).body), /This PR has no CI checks/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('the main branch CI row is informational during batch trials', async () => {
  const { dir, component, render } = await load('PRChecks')
  try {
    const checks = [{ name: 'CI / go-test', state: 'success' }]
    const row = (main: MergeItem['main'] | null) => text(render(component, { props: { checks, pr: {}, main } }).body)
    const sha = '0a135147deadbeef'
    assert.match(row({ state: 'pending', head: sha }), /main branch CI is running at 0a13514; the batch trial must pass/)
    assert.match(row({ state: 'failed', head: sha }), /main branch CI failed at 0a13514; the batch trial must pass/)
    assert.doesNotMatch(row({ state: 'passed', head: sha }), /main branch/)
    assert.doesNotMatch(row(null), /main branch/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('the chip and the sentence agree when reviewState is changes', () => {
  const c = { changeId: 'a', state: 'open', ci: 'running', reviewState: 'changes' }
  assert.equal(asksChanges(c), true)
  assert.equal(classify(c).label, 'Changes asked')
  assert.match(statusBox(c).say, /asked for changes/)
  assert.equal(asksChanges({ verdict: 'changes' }), true)
  assert.equal(asksChanges({ reviewState: 'reviewing' }), false)
})

test('botKey matches the backend rules', () => {
  assert.equal(botKey('coderabbit'), 'coderabbitai')
  assert.equal(botKey('CodeRabbitAI[bot]'), 'coderabbitai')
  assert.equal(botKey('copilot-pull-request-reviewer[bot]'), 'copilot')
  assert.equal(botKey('chatgpt-codex-connector'), 'codex')
  assert.equal(botKey('dependabot[bot]'), 'dependabot')
  assert.equal(botKey(null), '')
})

test('moveKey stays equal for a new array and changes when a change moves', () => {
  const a: Row[] = [{ changeId: '1', head: 'x', verdict: '', checked: '1' }]
  const b: Row[] = [{ changeId: '1', head: 'x', verdict: '', checked: '2' }]
  assert.equal(moveKey(a), moveKey(b))
  assert.notEqual(moveKey(a), moveKey([{ ...a[0], head: 'y' }]))
  assert.notEqual(moveKey(a), moveKey([{ ...a[0], verdict: 'changes' }]))
  assert.notEqual(moveKey(a), moveKey([{ ...a[0], ci: 'failed' }]))
  assert.notEqual(moveKey(a), moveKey([{ ...a[0], isDraft: true }]))
  assert.equal(moveKey(null), '')
})

test('the list shows CI and review side by side, and ready only on the same head', () => {
  assert.deepEqual(classify(open({ reviewState: 'reviewing' })), { group: 'review', label: 'Reviewing · CI running', tone: 'iris' })
  assert.deepEqual(classify(open({ verdict: 'approve', reviewState: 'approved' })), { group: 'ci', label: 'Approved · CI running', tone: 'run' })
  assert.equal(classify(ready({ head: 'b', reviewHead: 'a' })).group, 'review')
  assert.equal(statusBox(open({ reviewState: 'reviewing' })).say, 'The reviewer is reading this PR now. CI runs at the same time.')
})


test('the merge box has a checks, a review and a merge row', () => {
  const rows = (c: Row, o: Parameters<typeof mergeBox>[1]) => mergeBox(c, o).rows.map((r) => [r.id, r.icon, r.title])
  const c = open({ ci: 'pending', verdict: 'approve', reviewState: 'approved', reviewRound: 1, checks: chk('success', 'success', 'pending') })
  assert.deepEqual(rows(c, { state: mergeState(c, { state: 'ci' }) }), [
    ['checks', 'run', 'Some checks are still running'],
    ['review', 'ok', 'Approved by the reviewer'],
    ['merge', 'run', 'Waiting for CI'],
  ])
  const box = mergeBox(c, { state: mergeState(c, { state: 'ci' }) })
  assert.equal(need(box.rows[0]).note, '2 of 3 checks passed · c2 running')
  assert.equal(need(box.rows[0]).link, 'checks')
  assert.equal(need(box.rows[1]).link, 'review')
  // Before the first review round there is nothing to read.
  assert.equal(need(mergeBox(open({ reviewState: 'queued' })).rows[1]).link, '')
  // A failed check names itself; a queued PR shows its place.
  const red = open({ ci: 'failed', checks: chk('success', 'failure') })
  assert.deepEqual(mergeBox(red, { state: mergeState(red, { state: 'ci-failed', reason: 'CI failed: c1' }) }).rows[0], { id: 'checks', icon: 'bad', title: '1 check failed', note: 'c1', link: 'checks' })
  const r = ready({ checks: chk('success') })
  assert.deepEqual(rows(r, { state: mergeState(r, { state: 'ready' }, 2) })[2], ['merge', 'ok', 'Ready to merge'])
  assert.equal(need(mergeBox(r, { state: mergeState(r, { state: 'ready' }, 1) }).rows[2]).note, 'The merge service will test this PR in the next batch.')
  // Changes requested and a review in progress.
  assert.deepEqual(rows(open({ verdict: 'changes', reviewState: 'changes', checks: [] }), {})[1], ['review', 'bad', 'Changes requested'])
  assert.deepEqual(rows(open({ reviewState: 'reviewing', checks: [] }), {})[1], ['review', 'run', 'The reviewer is reading this PR'])
  assert.deepEqual(rows(open({ checks: [] }), {})[0], ['checks', 'wait', 'No checks yet'])
})

test('the merge box does not call cancelled or unfinished checks passed', () => {
  const box = (states: string[]) => mergeBox(open({ checks: chk(...states) }), {}).rows[0]
  assert.deepEqual(box(['success', 'cancelled']), { id: 'checks', icon: 'run', title: 'Some checks did not finish', note: '1 of 2 checks passed · c1 cancelled', link: 'checks' })
  assert.deepEqual(box(['success', 'skipped']), { id: 'checks', icon: 'ok', title: 'All checks passed', note: '1 of 2 checks passed · 1 skipped', link: 'checks' })
})

test('a reviewer session opens by id even though it is no member', () => {
  // A pool reviewer is a service outside the room hierarchy, so the row link opens it by id alone.
  assert.deepEqual(reviewerRef(open({ reviewerSession: 'ses_r', reviewerName: 'reviewer-9' }), 'room_1'), { id: 'ses_r', name: 'reviewer-9', hue: 0, roomId: 'room_1' })
  assert.equal(reviewerRef(open({ reviewerSession: 'ses_r' }), 'room_1')?.name, 'reviewer of #1')
  assert.equal(reviewerRef(open({}), 'room_1'), null)
})

test('the overlap list names conflicting files before shared files', async () => {
  const { dir, component, render } = await load('PROverlaps')
  try {
    const t = text(render(component, { props: { overlaps: [{ changeId: 'a', number: 7, files: ['b.go', 'a.go'], conflicts: ['a.go'] }] } }).body)
    assert.match(t, /Overlaps #7/)
    assert.ok(t.indexOf('a.go conflicts') < t.indexOf('b.go'))
    assert.equal(text(render(component, { props: {} }).body).trim(), '')
  } finally { await rm(dir, { recursive: true, force: true }) }
})
