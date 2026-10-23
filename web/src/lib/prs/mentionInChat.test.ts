import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from './prDetailHarness.ts'
import type { Row } from './prlist.ts'
import { element, elements, need } from '../../../test/need.ts'

// Svelte effects and the stubbed API promises both settle on microtasks, so a
// few turns of the microtask queue are enough and no timer is involved.
const settle = async () => { for (let turn = 0; turn < 8; turn++) await Promise.resolve() }
const API = `export const api = {
  githubList: () => Promise.resolve({ repo: 'o/r', items: [] }),
  githubView: (_r, _k, n) => Promise.resolve({ number: n, title: 'Speed up the parser', body: 'Body', comments: [], commits: [] }),
  mergeControlStatus: () => Promise.resolve({ roomId: 'r', paused: false, queue: [], trials: [], localCIChecks: {} }),
  feedAround: () => Promise.resolve({ posts: [] }),
  changeDetail: () => Promise.resolve({}),
}; export const openExternal = () => {}`
const code = await bundle({ component: 'GitHub', api: API })
const change: Row = { changeId: 'c', key: 'k', number: 7, kind: 'github', title: 'Speed up the parser', state: 'open', checks: [], branch: 'speed-up', base: 'main', url: 'https://github.com/o/r/pull/7', author: 'sam' }
const mentionButtons = (root: ParentNode) => elements(root, 'button').filter((b) => b.textContent?.trim() === 'Mention in chat')
const mentionIcons = (root: ParentNode) => elements(root, 'button').filter((b) => b.getAttribute('aria-label')?.startsWith('Mention ') === true)

test('a PR row and its detail page both mention the PR in chat', async () => {
  const mentions: unknown[] = []
  const dom = installDom({ ResizeObserver: class { observe() {} unobserve() {} disconnect() {} } })
  dom.reconfigure({ url: 'http://localhost/' })
  Object.assign(globalThis, { history: dom.window.history, location: dom.window.location })
  const { dir, default: GitHub, mount } = await importBundle(code, 'GitHub')
  const target = need(document.getElementById('app'), '#app')
  mount(GitHub, { target, props: { roomId: 'r', kind: 'pr', changes: [change], onmention: (reference: unknown) => mentions.push(reference) } })
  try {
    await settle()
    await settle()
    // The list row has one compact icon button, so the row keeps one line.
    const row = need(elements(target, '.item').find((item) => item.textContent?.includes('Speed up the parser')))
    assert.equal(mentionButtons(row).length, 0, 'the row has no wide Mention in chat button')
    need(mentionIcons(row)[0]).click()
    assert.deepEqual(mentions[0], { kind: 'pr', number: 7, title: 'Speed up the parser', state: 'open', url: 'https://github.com/o/r/pull/7' })
    // Opening the row keeps one in the detail header.
    element(target, '.row').click()
    await settle()
    const header = element(target, 'header.head')
    need(mentionButtons(header)[0]).click()
    assert.equal(mentions.length, 2)
    assert.deepEqual(mentions[1], mentions[0])
  } finally {
    dom.window.close()
    for (const name of [...domNames, 'history', 'location']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})
