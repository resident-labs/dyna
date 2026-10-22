import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('./Menu.svelte', import.meta.url), 'utf8')

interface MenuItem {
  disabled?: boolean
  run: () => void
}

interface MenuState extends Record<string, unknown> {
  pick: number
  items: MenuItem[]
  box: { querySelectorAll: () => { focus: () => void }[] }
  onclose: () => void
}

test('selection action menus skip disabled items in both directions and cannot invoke them', () => {
  const focused: number[] = []
  const runs: number[] = []
  const items: MenuItem[] = [{ run: () => runs.push(0) }, { disabled: true, run: () => runs.push(1) }, { run: () => runs.push(2) }]
  const state: MenuState = {
    pick: 0,
    items,
    box: { querySelectorAll: () => [0, 1, 2].map((i) => ({ focus: () => focused.push(i) })) },
    onclose: () => {},
  }
  runFunctions(source, ['focusAt', 'choose'], state)
  call(state, 'focusAt', 1)
  assert.equal(state.pick, 2)
  call(state, 'focusAt', 1, -1)
  assert.equal(state.pick, 0)
  call(state, 'focusAt', -1, -1)
  assert.equal(state.pick, 2)
  const disabled = items[1]
  const first = items[0]
  assert.ok(disabled !== undefined)
  assert.ok(first !== undefined)
  call(state, 'choose', disabled)
  assert.deepEqual(runs, [])
  call(state, 'choose', first)
  assert.deepEqual(runs, [0])
})
