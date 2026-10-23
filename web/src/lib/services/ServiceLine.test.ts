import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import test from 'node:test'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import type { Row } from './model.ts'

const code = await bundle({ component: '../services/ServiceLine' })

const row = (over: Partial<Row>): Row => ({
  id: 'update', name: 'Update Dyna', state: 'on', tone: 'on', detail: 'When main updates · last passed',
  toggle: { id: 'update.enabled', on: true }, profile: null, value: '', last: '',
  nextAt: '', nextText: '', filterable: true, ...over,
})

// mount wires the list to a fake page: onWrite records each call and can
// refuse or hold it, so a test controls the order of the answers.
async function mount(rows: Row[], onWrite: (id: string, value: string | boolean) => Promise<void>) {
  const dom = installDom()
  const { dir, default: C, mount, unmount, flushSync } = await importBundle(code, '../services/ServiceLine')
  const app = mount(C, { target: document.getElementById('app'), props: { rows, onWrite, onRun: async () => {} } })
  const root = document.getElementById('app')
  assert.ok(root)
  // The handlers await the fake page, so a few microtask turns reach the
  // rendered answer. No real timer is involved.
  const settle = async () => { for (let i = 0; i < 6; i++) await Promise.resolve(); flushSync() }
  const done = async () => {
    unmount(app)
    dom.window.close()
    for (const n of domNames) delete (globalThis as Record<string, unknown>)[n]
    await rm(dir, { recursive: true, force: true })
  }
  const line = (name: string): HTMLElement => {
    const found = [...root.querySelectorAll<HTMLElement>('.row')].find((li) => li.textContent?.includes(name))
    assert.ok(found, `missing ${name}`)
    return found
  }
  return { root, line, settle, done }
}

const switchOf = (line: HTMLElement): HTMLElement => {
  const found = line.querySelector<HTMLElement>('[role="switch"]')
  assert.ok(found, 'missing switch')
  return found
}

test('a switch write sends the flipped value and keeps a failed write on its own line', async () => {
  const writes: [string, string | boolean][] = []
  let fail = false
  const page = await mount([row({}), row({ id: 'voice', name: 'Voice', toggle: null, profile: null })], async (id, value) => {
    writes.push([id, value])
    if (fail) throw new Error('admin scope required')
  })
  try {
    switchOf(page.line('Update Dyna')).click()
    await page.settle()
    assert.deepEqual(writes, [['update.enabled', false]], 'the switch sends the value it does not have yet')

    fail = true
    switchOf(page.line('Update Dyna')).click()
    await page.settle()
    assert.match(page.line('Update Dyna').querySelector('.err')?.textContent ?? '', /admin scope required/, 'the failing line shows the engine message')
    assert.equal(page.root.querySelectorAll('.err').length, 1, 'the other rows stay clean')
  } finally { await page.done() }
})

test('a profile that did not save goes back to the stored choice', async () => {
  const page = await mount([row({
    id: 'reviewer', name: 'PR reviewers', toggle: null,
    profile: { id: 'review.profile', value: 'sol', options: ['', 'luna', 'sol'] },
  })], async () => { throw new Error('engine said no') })
  try {
    const select = page.line('PR reviewers').querySelector<HTMLSelectElement>('select')
    assert.ok(select, 'missing profile menu')
    select.value = 'luna'
    select.dispatchEvent(new Event('change', { bubbles: true }))
    await page.settle()
    assert.equal(select.value, 'sol', 'the menu shows the stored choice again')
    assert.match(page.line('PR reviewers').querySelector('.err')?.textContent ?? '', /engine said no/)
  } finally { await page.done() }
})

test('a slow write on one line leaves the other lines free', async () => {
  let release = (): void => {}
  const held = new Promise<void>((r) => { release = r })
  const page = await mount([
    row({}),
    row({ id: 'reviewer', name: 'PR reviewers', toggle: { id: 'review.enabled', on: true }, profile: null }),
  ], async (id) => { if (id === 'update.enabled') await held })
  try {
    switchOf(page.line('Update Dyna')).click()
    await page.settle()
    assert.equal(switchOf(page.line('Update Dyna')).hasAttribute('disabled'), true, 'the held line is still saving')

    switchOf(page.line('PR reviewers')).click()
    await page.settle()
    assert.equal(switchOf(page.line('PR reviewers')).hasAttribute('disabled'), false, 'the finished line is free')
    assert.equal(switchOf(page.line('Update Dyna')).hasAttribute('disabled'), true, 'and it stays free of the held line')

    release()
    await page.settle()
    assert.equal(switchOf(page.line('Update Dyna')).hasAttribute('disabled'), false, 'the held line frees when its write ends')
  } finally { await page.done() }
})
