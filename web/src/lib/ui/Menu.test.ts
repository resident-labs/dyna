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
    reveal: () => {},
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

test('menu bounds use the visual viewport and selection scrolls only its item list', () => {
  const style: Record<string, string> = {}
  const list = { scrollTop: 20, getBoundingClientRect: () => ({ top: 80, bottom: 200 }) }
  const state: Record<string, unknown> = {
    x: 400, y: 500, pos: {}, list,
    window: { visualViewport: { width: 240, height: 260, offsetLeft: 30, offsetTop: 40 } },
    box: { style, getBoundingClientRect: () => ({ width: 224, height: 244 }) },
  }
  runFunctions(source, ['place', 'reveal'], state)
  call(state, 'place')
  assert.equal(style.maxWidth, '224px')
  assert.equal(style.maxHeight, '244px')
  assert.deepEqual(JSON.parse(JSON.stringify(state.pos)), { left: 38, top: 48 })
  call(state, 'reveal', { getBoundingClientRect: () => ({ top: 180, bottom: 224 }) })
  assert.equal(list.scrollTop, 44)
  call(state, 'reveal', { getBoundingClientRect: () => ({ top: 60, bottom: 90 }) })
  assert.equal(list.scrollTop, 24)
})
