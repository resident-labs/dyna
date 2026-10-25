// A sample page: one complete demo pull request (description, comments, a
// review, a reviewer round, commits, checks and a diff) in the real PRDetail.
// It never contacts the engine: every api method is replaced with a stub
// before mounting, and the page answers from its demo data. Open
// /tests/pr-detail.html on the Vite dev server to check PR views;
// ?screen=test runs the interactions that call the api and reports in
// window.fixtureResult that no engine request was made.
import '../src/app.css'
import '../src/lib/ui/tokens.css'
import { mount } from 'svelte'
import PRDetail from '../src/lib/prs/PRDetail.svelte'
import { api } from '../src/api.ts'
import { DEFAULT_POLICY } from '../src/lib/prs/pr.ts'
import type { Row, Names } from '../src/lib/prs/prlist.ts'
import type { ReviewRound } from '../src/lib/api/change.ts'
import type { GitHubDetail } from '../src/lib/api/github.ts'
import { need } from '../test/need.ts'

const at = (m: number) => new Date(Date.UTC(2026, 9, 19, 9, m)).toISOString()
const change: Row = {
  isDraft: false, created: at(0), checked: at(0), detailVersion: '', queuePosition: null, botReviews: null,
  changeId: 'demo-42', key: 'owner/repo#42', kind: 'github', roomId: 'demo', number: 42, repo: 'owner/repo',
  title: 'Fade the PR page tabs and ease the underline', state: 'open', status: 'changes-requested', ci: 'failed',
  verdict: 'changes', reviewState: 'changes', author: 'dm-redesign', baseRef: 'main', branch: 'pr-tab-motion',
  checks: [
    { name: 'frontend', state: 'success', url: 'https://example.invalid/checks/1' },
    { name: 'go-test', state: 'success', url: 'https://example.invalid/checks/2' },
    { name: 'cross-build', state: 'failure', url: 'https://example.invalid/checks/3', failureExcerpt: 'FAIL: dist/linux-arm64: exit status 1' },
  ],
}
const detail: GitHubDetail = {
  kind: 'pr', number: 42, title: change.title ?? '', state: 'open', workingSessionLive: false, updatedAt: at(0), checks: null, files: null,
  author: { login: 'dm-redesign' }, createdAt: at(0), url: 'https://example.invalid/pull/42',
  body: 'Fades each tab panel in and eases the selected underline.\n\nThe Files tab stays mounted, so its diff survives a tab switch.',
  labels: [{ name: 'ui', color: '8888ff' }, { name: 'motion', color: 'b7a8ff' }],
  comments: [
    { author: { login: 'ui-guy-right' }, body: 'Keep PRFiles outside the keyed block, or every switch reloads the diff.', createdAt: at(5) },
    { author: { login: 'dm-redesign' }, body: 'Done: it stays mounted and only its wrapper fades.', createdAt: at(12) },
  ],
  reviews: [{ author: { login: 'reviewer' }, state: 'APPROVED', body: 'Looks right.', submittedAt: at(30) }],
  commits: [
    { sha: '5f1c2a7d9e', headline: 'Fade the PR page tab panel in', author: { login: 'dm-redesign', name: 'dm-redesign' }, date: at(2) },
    { sha: '9b04e3c1aa', headline: 'Ease the tab underline and colour', author: { login: 'dm-redesign', name: 'dm-redesign' }, date: at(8) },
    { sha: 'ca99b33f4d', headline: 'Keep PRFiles mounted outside the keyed panel', author: { login: 'dm-redesign', name: 'dm-redesign' }, date: at(14) },
  ],
  changedFiles: 2, additions: 5, deletions: 1,
}
const diff = `diff --git a/web/src/lib/prs/PRDetail.svelte b/web/src/lib/prs/PRDetail.svelte
--- a/web/src/lib/prs/PRDetail.svelte
+++ b/web/src/lib/prs/PRDetail.svelte
@@ -99,3 +99,5 @@
-      {#if filesSeen && number}<div hidden={tab !== 'files'}><PRFiles {roomId} {number} /></div>{/if}
+      {#if filesSeen && number}<div class="tabpanel" hidden={tab !== 'files'}><PRFiles {roomId} {number} /></div>{/if}
+      {#key tab}<div class="tabpanel">
       {#if tab === 'commits' && number}
diff --git a/web/src/lib/prs/prTabMotion.test.js b/web/src/lib/prs/prTabMotion.test.js
--- /dev/null
+++ b/web/src/lib/prs/prTabMotion.test.js
@@ -0,0 +1,3 @@
+import test from 'node:test'
+import assert from 'node:assert/strict'
+// Tab switches fade the new panel in.
`
const rounds: ReviewRound[] = [{ round: 1, at: at(20), head: '', verdict: 'changes', summary: 'The underline snaps on a fast click.\n\n- **Should fix** (`PRDetail.svelte:136`): Ease the underline colour too.', findings: [{ id: 'f1', severity: 'should-fix', file: 'web/src/lib/prs/PRDetail.svelte', line: 136, why: 'Ease the underline colour too.', fix: 'Add border-color to the transition.', replies: null }] }]

// Demo answers for the calls the page makes; every other api method returns
// nothing. Each call is recorded, and so is any /api request that slips past.
const answers: Record<string, (...args: unknown[]) => unknown> = {
  changeDetail: () => ({ ...change, reviewRounds: rounds, findings: need(rounds[0]).findings, notes: {} }),
  githubDiff: () => ({ text: diff, truncated: false }),
  roomChangePolicy: () => DEFAULT_POLICY,
  setChangePolicyOverride: () => ({}),
}
const calls: string[] = [], engine: string[] = []
for (const name of Object.keys(api)) {
  if (typeof Reflect.get(api, name) === 'function') Reflect.set(api, name, async (...args: unknown[]) => { calls.push(name); return answers[name]?.(...args) ?? null })
}
const realFetch = window.fetch
window.fetch = (input: RequestInfo | URL, init?: RequestInit) => { if (String(input).includes('/api')) engine.push(String(input)); return realFetch(input, init) }

const names: Names = {}
const props = $state({ change, detail, roomId: 'demo', names, tab: 'conversation' })
mount(PRDetail, { target: need(document.getElementById('app'), 'app'), props })
// The page exposes its tab so a browser check can switch tabs directly.
Reflect.set(window, 'showTab', (tab: string) => { props.tab = tab })

// ?screen=test: open the rules drawer, fold and unfold the review, and report.
if (new URLSearchParams(location.search).get('screen') === 'test') {
  const wait = () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)))
  const button = (text: string) => [...document.querySelectorAll('button')].find((b) => b.textContent?.trim() === text)
  const pageText = () => need(document.getElementById('app'), 'app').textContent ?? ''
  const copies = (text: string) => pageText().split(text).length - 1
  await wait()
  button('Rules for this PR')?.click(); await wait()
  document.querySelector<HTMLElement>('[aria-label^="Close"]')?.click(); await wait()
  // The review starts open with each finding once. One control folds it and opens it again.
  const expanded = copies('Ease the underline colour too.') === 1 && copies('Add border-color to the transition.') === 1
  button('Hide review')?.click(); await wait()
  const folded = copies('Add border-color to the transition.') === 0
  button('Show review')?.click(); await wait()
  const reopened = copies('Add border-color to the transition.') === 1
  // pass: the drawer and review reached their stubs, and nothing reached the engine.
  const pass = engine.length === 0 && calls.includes('roomChangePolicy') && expanded && folded && reopened
  Reflect.set(window, 'fixtureResult', { pass, calls: [...calls], engineRequests: [...engine] })
}
