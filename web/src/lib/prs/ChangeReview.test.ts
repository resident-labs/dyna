import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import type { Finding } from '../api/change.ts'
import { need } from '../../../test/need.ts'
import { verdictMeta, statusMeta, refName, changeName, changeTitle, groupFindings, prAttention, prStory } from './pr.ts'

// Render the real ChangeReview on the server and read the text.
async function load(name: string) {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-reviewer-ui-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: `export { default } from './${name}.svelte'; export { render } from 'svelte/server'`, resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [svelteEsbuildPlugin('server'), { name: 'review-fixture', setup(builder) {
      builder.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, `${name}.mjs`)
  await writeFile(file, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render }
}

const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')
const f = (id: string, severity: string, extra: Partial<Finding> = {}): Finding => ({ id, severity, file: 'a/b.go', line: 7, why: `why ${id}`, fix: `fix ${id}`, replies: [], ...extra })

test('verdict labels: nits read as approved with a note, a question is not changes', () => {
  assert.deepEqual(verdictMeta('approve'), { label: 'Approved', tone: 'ok', note: '' })
  const nits = need(verdictMeta('approve-with-nits'))
  assert.equal(nits.label, 'Approved')
  assert.equal(nits.tone, 'ok')
  assert.match(nits.note, /nits/i)
  const q = need(verdictMeta('question'))
  assert.equal(q.label, 'Question')
  assert.notEqual(q.tone, 'ok')
  assert.equal(verdictMeta('changes')?.label, 'Changes requested')
  assert.equal(verdictMeta(''), null)
  // The watcher sends question as changes and nits as approved.
  assert.equal(statusMeta({ status: 'changes-requested', verdict: 'question' }).label, 'Question')
  assert.equal(statusMeta({ status: 'approved', verdict: 'approve-with-nits' }).label, 'Approved with nits')
  assert.equal(statusMeta({ status: 'approved', ci: 'passed', reviewState: 'approved', verdict: 'approve-with-nits' }).label, 'Approved with nits: ready to merge')
  assert.equal(statusMeta({ status: 'changes-requested', reviewState: 'changes', verdict: 'question' }).label, 'Question for the author')
  assert.equal(statusMeta({ status: 'changes-requested', reviewState: 'changes', verdict: 'changes' }).label, 'Changes requested')
})

test('local changes show branch and base, not a PR number', () => {
  const local = { kind: 'local', branch: 'feat/x', base: 'main', number: 0 }
  assert.equal(changeName(local), 'feat/x → main')
  assert.equal(changeName({ kind: 'github', number: 12 }), '#12')
  assert.equal(changeTitle(local), 'feat/x')
  // The host sends a commit in base and the branch name in baseRef.
  const sha = 'fc3cd54832db7da2152ff24d9c303b5a83759d28'
  assert.equal(changeName({ ...local, base: sha, baseRef: 'main' }), 'feat/x → main')
  assert.equal(changeName({ ...local, base: sha }), 'feat/x → fc3cd54')
  assert.equal(refName('main'), 'main')
  // A local draft shows source approval; merging requires an attached PR and GitHub CI.
  assert.equal(statusMeta({ kind: 'local', state: 'open', reviewState: 'approved', verdict: 'approve' }).label, 'Approved')
  assert.equal(statusMeta({ ...local, ci: 'passed', state: 'open', reviewState: 'approved', verdict: 'approve' }).label, 'Approved')
  assert.equal(statusMeta({ state: 'open', reviewState: 'queued', queuePosition: 1 }).label, 'Queued for review (1st)')
})

test('findings group by severity, heaviest first, and skip empty groups', () => {
  const groups = groupFindings([f('1', 'nit'), f('2', 'blocking'), f('3', 'should-fix'), f('4', 'blocking'), f('5', 'odd')])
  assert.deepEqual(groups.map((g) => [g.id, g.items.map((x) => x.id)]), [['blocking', ['2', '4']], ['should-fix', ['3']], ['nit', ['1', '5']]])
  assert.deepEqual(groupFindings([f('1', 'nit')]).map((g) => g.id), ['nit'])
  assert.deepEqual(groupFindings(undefined), [])
})

test('change review renders notes, severity groups, patch toggle and replies', async () => {
  const { dir, component, render } = await load('ChangeReview')
  try {
    const change = {
      changeId: 'c1', verdict: 'changes',
      notes: { intent: 'Queue by head.', testing: 'go test passes.', risks: '', questions: 'Drop a started review?' },
      findings: [f('n1', 'nit'), f('b1', 'blocking', { patch: '+x', replies: [{ disposition: 'wont-fix', message: 'Not needed here.', head: 'abcdef123', author: 'owner', at: '2026-10-17T12:00:00Z' }] }), f('s1', 'should-fix')],
      reviewRounds: [{ round: 1, head: 'abc1234', verdict: 'approve-with-nits', summary: 's', findings: [] }, { round: 2, head: 'def5678', verdict: 'question', summary: 's', findings: [f('x', 'nit')] }],
    }
    const html = render(component, { props: { change, roomId: 'r' } }).body
    const t = text(html)
    assert.match(t, /Author's notes Intent Queue by head\. Testing go test passes\. Questions for the reviewer Drop a started review\?/)
    assert.equal(/Risks/.test(t), false)
    assert.match(t, /Blocking 1.*a\/b\.go:7 why b1 Fix fix b1/)
    assert.ok(t.indexOf('Blocking') < t.indexOf('Should fix') && t.indexOf('Should fix') < t.indexOf('Nit'))
    assert.match(t, /Show patch/)
    assert.equal(html.includes('class="patch'), false)
    assert.match(t, /Won't fix owner at abcdef1.*Not needed here\./)
    // Authors answer reviewer findings; the owner gets no reply buttons for them.
    assert.doesNotMatch(html, /<button[^>]*>\s*(Fixed|Won't fix|Won&#39;t fix|Question)\s*<\/button>/)
    assert.match(t, /Round 2 Question.*Round 1 Approved with nits/)
    // The owner's own comment has one Resolve button.
    const own = text(render(component, { props: { change: { ...change, findings: [f('o1', 'owner')] }, roomId: 'r' } }).body)
    assert.match(own, /Resolve/)
    assert.doesNotMatch(text(render(component, { props: { change: { ...change, findings: [f('o1', 'owner')] }, roomId: '' } }).body), /Resolve/)
    assert.doesNotMatch(text(render(component, { props: { change: { verdict: 'approve', findings: [], reviewRounds: [] }, roomId: 'r' } }).body), /No findings/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('a session row gets a dot only when its PR needs someone, and the profile says why in sentences', () => {
  assert.equal(prAttention({ number: 9, status: 'ci-running' }), null)
  assert.equal(prAttention(null), null)
  const reason = 'The reviewer stopped early: too-large.'
  const blocked = { number: 9, status: 'ci-running', reviewState: 'blocked', reviewBlockKind: 'reviewer-stopped' as const, reviewBlocked: reason }
  assert.deepEqual(prAttention(blocked), { tone: 'warning', label: `#9: ${reason}` })
  assert.equal(prAttention({ number: 9, status: 'ci-failed' })?.tone, 'danger')
  assert.equal(prStory({ number: 9, status: 'ci-failed', ci: 'failed' }).review, 'Review waits for CI to pass.')
  assert.deepEqual(prStory(blocked), { review: reason, ci: 'Running.', routine: false })
  assert.deepEqual(prStory({ number: 9, reviewState: 'queued', queuePosition: 2, ci: 'failed', ciReason: 'go test timed out' }), { review: 'Waiting for a reviewer, 2nd in line.', ci: 'Failed: go test timed out.', routine: false })
  // A head refresh after a push clears by itself: no dot, muted words.
  const refresh = { ...blocked, reviewBlockKind: 'head-refresh' as const, reviewBlocked: 'The PR head is unverified; waiting for a successful refresh.' }
  assert.equal(prAttention(refresh), null)
  assert.deepEqual(prStory(refresh), { review: 'Checking the latest push.', ci: 'Running.', routine: true })
  // An approval promises a merge only when Dyna merges and only CI is left.
  const approved = { number: 9, reviewState: 'approved', state: 'open', ci: 'passed', head: 'b', reviewHead: 'b' }
  assert.equal(prStory(approved).review, 'Approved and ready to merge.')
  assert.equal(prStory({ ...approved, ci: 'pending' }).review, 'Approved. It merges when CI passes.')
  assert.equal(prStory({ ...approved, reviewHead: 'a' }).review, 'An earlier push was approved; the latest push needs review.')
  assert.equal(prStory({ ...approved, missingRequiredReviewers: ['ari'] }).review, 'Approved. It still needs a review from ari.')
  assert.equal(prStory({ ...approved, upstreamPr: true }).review, 'Approved. Maintainers of the upstream repository merge it.')
})
