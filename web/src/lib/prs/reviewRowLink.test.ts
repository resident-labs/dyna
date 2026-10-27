import test from 'node:test'
import assert from 'node:assert/strict'
import { bundle, domNames, importBundle, installDom } from './prDetailHarness.ts'
import { need, textOf } from '../../../test/need.ts'
import { mergeBox, type Row } from './prlist.ts'

// While a reviewer session works, the review row links to that live session —
// there is no new review to read yet — the way the workflow run view links
// each worker to its session. Once the review is done, the row keeps its
// 'Read the review' link.
const code = await bundle({ component: 'MergeBox' })
const open = (o: Row = {}): Row => ({ changeId: 'c', kind: 'pr', number: 1, state: 'open', status: 'ci-running', ci: 'pending', ...o })

async function render(c: Row) {
  const dom = installDom()
  const { default: MergeBox, mount } = await importBundle(code, 'MergeBox')
  const target = need(document.getElementById('app'), '#app')
  const opened: string[] = []
  const read: string[] = []
  mount(MergeBox, { target, props: { change: c, box: mergeBox(c, {}), onread: () => read.push('review'), onreviewer: () => opened.push('session') } })
  return { target, opened, read, done: () => { dom.window.close(); for (const name of domNames) Reflect.deleteProperty(globalThis, name) } }
}

const click = (target: HTMLElement, label: string) => {
  need([...target.querySelectorAll('button')].find((b) => b.textContent === label), `button ${label}`).click()
}

test('the review row opens the reviewer session while it works, the review once it is done', async () => {
  const reading = await render(open({ reviewState: 'reviewing', reviewerSession: 'ses_r', reviewRound: 3, checks: [] }))
  try {
    const text = textOf(reading.target).replace(/\s+/g, ' ')
    assert.match(text, /The reviewer is reading this PR/)
    assert.match(text, /Open reviewer session/)
    assert.doesNotMatch(text, /Read the review/)
    click(reading.target, 'Open reviewer session')
    assert.deepEqual(reading.opened, ['session'])
    assert.deepEqual(reading.read, [])
  } finally { reading.done() }
  const done = await render(open({ status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', reviewRound: 1, reviewerSession: 'ses_r', checks: [] }))
  try {
    const text = textOf(done.target).replace(/\s+/g, ' ')
    assert.match(text, /Read the review/)
    assert.doesNotMatch(text, /Open reviewer session/)
    click(done.target, 'Read the review')
    assert.deepEqual(done.read, ['review'])
    assert.deepEqual(done.opened, [])
  } finally { done.done() }
})
