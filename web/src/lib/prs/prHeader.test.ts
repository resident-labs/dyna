import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from './prDetailHarness.ts'
import type { Row } from './prlist.ts'
import { element, elements, need, textOf } from '../../../test/need.ts'

const settle = () => new Promise((r) => setTimeout(r, 0))
// Bundle outside the test clock: only the mounted view is measured.
const code = await bundle()
const base: Row = { changeId: 'c', key: 'k', number: 7, kind: 'github', title: 'T', state: 'open', checks: [], branch: 'a', base: 'b' }

async function render(props: object) {
  const dom = installDom()
  const { dir, default: PRDetail, mount } = await importBundle(code)
  const target = need(document.getElementById('app'), '#app')
  mount(PRDetail, { target, props: { roomId: 'r', ...props } })
  await settle()
  return { target, done: () => { dom.window.close(); for (const n of domNames) Reflect.deleteProperty(globalThis, n); return rm(dir, { recursive: true, force: true }) } }
}
const head = (t: HTMLElement) => element(t, 'header.head')
const button = (t: ParentNode, label: string) => elements(t, 'button').find((b) => b.textContent?.trim() === label)

// The header is the title, number, GitHub state, one status, the branch line
// and the GitHub link. Author, reviewer and rules live in the merge box.
test('the header holds no author, reviewer or rules line', async () => {
  const change = { ...base, url: 'https://github.com/o/r/pull/7', authorSession: 's1', reviewerSession: 's2', reviewState: 'reviewing' }
  const { target, done } = await render({ change, names: { s1: 'engine-cpu', s2: 'reviewer-774' }, reviewerModel: 'sol', detail: { createdAt: 't', body: 'x', commits: [] }, onauthor: () => {}, onreviewer: () => {} })
  try {
    const h = textOf(head(target)).replace(/\s+/g, ' ')
    assert.doesNotMatch(h, /Author|Reviewer|engine-cpu|reviewer-774|Rules|room rules|sol/)
    assert.match(h, /T #7/)
    assert.doesNotMatch(h, /\bOpen\b/)
    assert.match(h, /a → b/)
    assert.equal(head(target).querySelectorAll('.pr-chip').length, 0, 'no redundant status label')
    assert.ok(button(head(target), 'GitHub ↗'))
    assert.equal(target.querySelector('.factsline'), null)
    // The description still names the author.
    assert.equal(textOf(target.querySelector('.post.first strong')), 'engine-cpu')
  } finally {
    await done()
  }
})

test('the author session and the rules stay one click away in the merge box', async () => {
  const calls: string[] = []
  const change = { ...base, authorSession: 's1' }
  const { target, done } = await render({ change, names: { s1: 'engine-cpu' }, detail: { createdAt: 't', body: 'x', commits: [] }, onauthor: () => calls.push('author') })
  try {
    need(button(target, 'Open the author session')).click()
    assert.deepEqual(calls, ['author'])
    need(button(target, 'Rules for this PR')).click()
    await settle()
    assert.ok(document.querySelector('[role=dialog], .drawer'), 'the rules drawer opens')
  } finally {
    await done()
  }
})

test('GitHub labels stay in the header', async () => {
  const { target, done } = await render({ change: { ...base }, names: {}, detail: { labels: [{ name: 'ui', color: '8888ff' }, { name: 'perf', color: 'ffaa00' }], commits: [] } })
  try {
    assert.deepEqual([...head(target).querySelectorAll('.ghlabel')].map((l) => l.textContent), ['ui', 'perf'])
  } finally {
    await done()
  }
})
