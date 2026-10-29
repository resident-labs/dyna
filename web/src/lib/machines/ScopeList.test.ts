import test from 'node:test'
import assert from 'node:assert/strict'
import { bundle, importBundle, installDom } from '../prs/prDetailHarness.ts'

const code = await bundle({ component: '../machines/ScopeList' })

async function mountList(props: Record<string, unknown>) {
  const dom = installDom()
  const ui = await importBundle(code, '../machines/ScopeList')
  const target = document.getElementById('app')
  assert.ok(target)
  const component = ui.mount(ui.default, { target, props })
  ui.flushSync()
  return { ui, component, dom }
}

test('an empty list says every room, and removing a chip reports the list without it', async () => {
  const changes: string[][] = []
  const { ui, component } = await mountList({ label: 'Rooms', values: [], everyText: 'Every room', options: [{ value: 'r1', label: 'dyna' }], onchange: (v: string[]) => changes.push(v) })
  assert.equal(document.querySelector('.every')?.textContent, 'Every room')
  ui.unmount(component)
  const withTwo = await mountList({ label: 'Rooms', values: ['r1', 'r2'], everyText: 'Every room', options: [{ value: 'r1', label: 'dyna' }, { value: 'r2', label: 'fl-rea' }], onchange: (v: string[]) => changes.push(v) })
  assert.deepEqual([...document.querySelectorAll('.chip .text')].map((n) => n.textContent), ['dyna', 'fl-rea'], 'chips show room names, not IDs')
  document.querySelector<HTMLButtonElement>('button[aria-label="Remove dyna"]')?.click()
  assert.deepEqual(changes, [['r2']])
  withTwo.ui.unmount(withTwo.component)
})

test('the room picker offers only rooms not chosen, and picking one adds it', async () => {
  const changes: string[][] = []
  const { ui, component } = await mountList({ label: 'Rooms', values: ['r1'], everyText: 'Every room', options: [{ value: 'r1', label: 'dyna' }, { value: 'r2', label: 'fl-rea' }], onchange: (v: string[]) => changes.push(v) })
  const select = document.querySelector<HTMLSelectElement>('select[aria-label="Add to Rooms"]')
  assert.ok(select)
  assert.deepEqual([...select.options].map((o) => o.value), ['', 'r2'])
  select.value = 'r2'
  select.dispatchEvent(new Event('change', { bubbles: true }))
  assert.deepEqual(changes, [['r1', 'r2']])
  ui.unmount(component)
})

test('typing a room and pressing Enter adds it once, trimmed', async () => {
  const changes: string[][] = []
  const { ui, component } = await mountList({ label: 'Rooms', values: ['dyna'], everyText: 'Every room', onchange: (v: string[]) => changes.push(v) })
  const input = document.querySelector<HTMLInputElement>('input[aria-label="Add to Rooms"]')
  assert.ok(input)
  input.value = ' jobs '
  input.dispatchEvent(new Event('input', { bubbles: true }))
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  assert.deepEqual(changes, [['dyna', 'jobs']])
  ui.unmount(component)
})

test('a read-only list keeps its chips and offers no way to change them', async () => {
  const { ui, component } = await mountList({ label: 'Rooms', values: ['r1'], everyText: 'Every room', options: [{ value: 'r1', label: 'dyna' }], readOnly: true, onchange: () => assert.fail('a read-only list changed') })
  assert.equal(document.querySelectorAll('.chip').length, 1)
  assert.equal(document.querySelectorAll('button, select, input').length, 0)
  ui.unmount(component)
})
