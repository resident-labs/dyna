import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import test from 'node:test'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import type { Row } from './model.ts'

const code = await bundle({ component: '../services/ServiceList' })

const row = (over: Partial<Row>): Row => ({
  id: 'update', name: 'Update Dyna', state: 'on', tone: 'on', detail: 'When main updates · last passed',
  profileStatus: '', toggle: { id: 'update.enabled', on: true }, profile: null, value: '', last: '',
  nextAt: '', nextText: '', filterable: true, ...over,
})

test('a switch write sends the flipped value and keeps a failed write on its own line', async () => {
  const dom = installDom()
  const { dir, default: C, mount, unmount, flushSync } = await importBundle(code, '../services/ServiceList')
  const writes: [string, string | boolean][] = []
  let fail = false
  const onWrite = async (id: string, value: string | boolean) => {
    writes.push([id, value])
    if (fail) throw new Error('admin scope required')
  }
  const app = mount(C, { target: document.getElementById('app'), props: { rows: [row({}), row({ id: 'voice', name: 'Voice', toggle: null, profile: null })], selected: '', onSelect: () => {}, onWrite, onRun: async () => {} } })
  const root = document.getElementById('app')
  assert.ok(root)
  const switchOf = (): HTMLElement => {
    const found = root.querySelector<HTMLElement>('.row [role="switch"]')
    assert.ok(found)
    return found
  }
  const settle = async () => { await new Promise((r) => setTimeout(r, 0)); flushSync() }
  try {
    switchOf().click()
    await settle()
    assert.deepEqual(writes, [['update.enabled', false]], 'the switch sends the value it does not have yet')

    fail = true
    switchOf().click()
    await settle()
    assert.match(root.querySelector('.row .err')?.textContent ?? '', /admin scope required/, 'the failing line shows the engine message')
    assert.equal(root.querySelectorAll('.err').length, 1, 'the other rows stay clean')
  } finally {
    unmount(app)
    dom.window.close()
    for (const n of domNames) delete (globalThis as Record<string, unknown>)[n]
    await rm(dir, { recursive: true, force: true })
  }
})
