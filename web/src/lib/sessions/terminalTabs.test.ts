import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

test('terminal tab Home and End reveal their tab while shell shortcuts keep focus', async () => {
  const source = readFileSync(new URL('./TermDock.svelte', import.meta.url), 'utf8')
  const list = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
  const context: Record<string, unknown> = {
    list, active: 'b', current: list[1], tick: () => Promise.resolve(),
    setTimeout: (fn: () => void) => fn(), focused: 'shell', ref: 'r', dock: { open: true }, narrow: false,
    rootEl: { contains: () => true }, Node: class {},
    act: (a: { type: string; id?: string }) => { if (a.type === 'tab') context.active = a.id },
  }
  const strip = {
    scrollLeft: 0,
    getBoundingClientRect: () => ({ left: 0, right: 100 }),
    querySelector: () => ({
      focus: () => { context.focused = context.active },
      closest: () => ({
        getBoundingClientRect: () => {
          const left = list.findIndex(t => t.id === context.active) * 60 - strip.scrollLeft
          return { left, right: left + 60 }
        },
      }),
    }),
  }
  context.tabsEl = strip
  runFunctions(source, ['revealTab', 'tabKey', 'shortcut'], context)
  const key = (name: string) => ({ key: name, preventDefault() {} })
  call(context, 'tabKey', key('Home'))
  await Promise.resolve()
  assert.equal(context.active, 'a')
  call(context, 'tabKey', key('End'))
  await Promise.resolve()
  assert.equal(context.active, 'c')
  assert.equal(context.focused, 'c')
  assert.equal(strip.scrollLeft, 80)
  context.focused = 'shell'
  call(context, 'shortcut', { ...key('PageUp'), ctrlKey: true, target: new (context.Node as new () => object)() })
  await Promise.resolve()
  assert.equal(context.active, 'b')
  assert.equal(context.focused, 'shell')
  call(context, 'shortcut', key('Home'))
  assert.equal(context.active, 'b', 'normal shell Home is not a tab shortcut')
})
