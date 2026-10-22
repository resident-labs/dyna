import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'

const entries = Array.from({ length: 30 }, (_, i) => ({ id: 'e' + i, name: 'Entry ' + String(i).padStart(2, '0'), kind: 'feature', parent: '', summary: 'Summary ' + i, owns: [], docs: [], tests: [], relates: [] }))
const api = `export const api = { library: () => Promise.resolve({ enabled: true, lint: [], stale: [], entries: ${JSON.stringify(entries)} }) }; export const openExternal = () => {}`
const code = await bundle({ component: '../library/Library', api })

async function mountLibrary(narrow: boolean) {
  const dom = installDom()
  const { dir, default: C, mount, unmount, flushSync } = await importBundle(code, '../library/Library')
  const app = mount(C, { target: document.getElementById('app'), props: { roomId: 'r', narrow } })
  const root = document.getElementById('app')
  assert.ok(root)
  await new Promise((r) => setTimeout(r, 0))
  flushSync()
  const done = async () => { unmount(app); dom.window.close(); for (const n of domNames) delete (globalThis as Record<string, unknown>)[n] }
  return { root, flushSync, done, dir }
}
const nodes = (root: HTMLElement) => [...root.querySelectorAll<HTMLButtonElement>('.tree .node')]

test('on a desktop the tree and the entry sit side by side, each in its own scroll area', async () => {
  const page = await mountLibrary(false)
  try {
    assert.ok(page.root.querySelector('.tree'))
    assert.ok(page.root.querySelector('.detail'))
    assert.equal(page.root.querySelector('.back'), null)
    nodes(page.root)[5]?.click()
    page.flushSync()
    assert.ok(page.root.querySelector('.tree'), 'the tree stays after picking an entry')
    assert.match(page.root.querySelector('.detail h2')?.textContent ?? '', /Entry 05/)
    const css = readFileSync(new URL('./Library.svelte', import.meta.url), 'utf8')
    assert.match(css, /\.tree[^{]*\{[^}]*overflow-y: auto/)
    assert.match(css, /\.detail[^{]*\{[^}]*overflow-y: auto/)
  } finally { await page.done() }
})

test('on a phone the tree opens first, an entry opens as its own view, and Back returns to the tree', async () => {
  const page = await mountLibrary(true)
  try {
    assert.ok(page.root.querySelector('.tree'))
    assert.equal(page.root.querySelector('.detail'), null, 'no detail until an entry is picked')
    nodes(page.root)[7]?.click()
    page.flushSync()
    assert.equal(page.root.querySelector('.tree'), null, 'the tree gives way to the entry')
    assert.match(page.root.querySelector('.detail h2')?.textContent ?? '', /Entry 07/)
    page.root.querySelector<HTMLButtonElement>('.back')?.click()
    await new Promise((r) => setTimeout(r, 0))
    page.flushSync()
    assert.ok(page.root.querySelector('.tree'))
    assert.equal(page.root.querySelector('.detail'), null)
  } finally { await page.done() }
})

test('Back puts the tree at the scroll place the reader left', async () => {
  const page = await mountLibrary(true)
  try {
    const tree = page.root.querySelector<HTMLElement>('.tree')
    assert.ok(tree)
    tree.scrollTop = 240
    nodes(page.root)[20]?.click()
    page.flushSync()
    const back = page.root.querySelector<HTMLButtonElement>('.back')
    assert.ok(back, 'the entry view has a Back button')
    back.click()
    await new Promise((r) => setTimeout(r, 0))
    page.flushSync()
    assert.equal(page.root.querySelector<HTMLElement>('.tree')?.scrollTop, 240)
  } finally { await page.done() }
})
