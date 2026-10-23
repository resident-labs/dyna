import test from 'node:test'
import assert from 'node:assert/strict'
import { bundle, domNames, importBundle, installDom } from './prDetailHarness.ts'
import { need, textOf } from '../../../test/need.ts'
import type { MergeControlStatus } from '../api/change.ts'

// The strip draws the waiting line from typed facts. It shortens the commit
// with the helper the merge card uses, and never prints the service's sentence
// or the full commit.
const code = await bundle({ component: 'MergingStrip' })
const head = 'e1d554e3f2a52ea46a2d575803f412b2cbac2514'

async function render(status: MergeControlStatus) {
  const dom = installDom()
  const { default: MergingStrip, mount } = await importBundle(code, 'MergingStrip')
  const target = need(document.getElementById('app'), '#app')
  const picked: string[] = []
  mount(MergingStrip, { target, props: { status, onpick: (id: string) => picked.push(id), onresume: () => {} } })
  return {
    target,
    picked,
    done: () => { dom.window.close(); for (const name of domNames) Reflect.deleteProperty(globalThis, name) },
  }
}

test('a waiting line names the failed main CI with its short commit and waiting PRs', async () => {
  const main = { state: 'failed', head, wait: { kind: 'failed' as const, base: 'main' } }
  const { target, done } = await render({ roomId: 'r', paused: false, queue: [
    { roomId: 'r', changeId: 'github.com/o/r#281', url: '', head: 'h1', state: 'trial-running', main },
    { roomId: 'r', changeId: 'github.com/o/r#280', url: '', head: 'h2', state: 'trial-running', main },
  ], trials: [] })
  try {
    const text = textOf(target).replace(/\s+/g, ' ')
    assert.match(text, /Default branch CI failed on main at e1d554e/)
    assert.match(text, /#281/)
    assert.match(text, /#280/)
    assert.ok(!text.includes(head), `the full commit reached the screen: ${text}`)
    assert.doesNotMatch(text, /GitHub CI failed on/, 'the service sentence reached the screen')
  } finally {
    done()
  }
})

test('a room without CI on its default branch says so without a commit', async () => {
  const main = { state: 'pending', head: '', wait: { kind: 'no-push-ci' as const } }
  const { target, done } = await render({ roomId: 'r', paused: false, queue: [
    { roomId: 'r', changeId: 'github.com/o/r#281', url: '', head: 'h1', state: 'ready', main },
  ], trials: [] })
  try {
    const text = textOf(target).replace(/\s+/g, ' ')
    assert.match(text, /No default-branch CI; the PR checks decide/)
    assert.doesNotMatch(text, /on +at/)
  } finally {
    done()
  }
})
