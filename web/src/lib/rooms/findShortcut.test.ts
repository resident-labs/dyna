import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { stripTypes } from '../../../test/svelte-source.ts'

// Run the App's own shortcut and openFind with a controlled state.
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
// The parser adds source offsets to every node; estree's types do not list them.
const offset = (node: object, key: 'start' | 'end'): number => {
  const v: unknown = Reflect.get(node, key)
  assert.equal(typeof v, 'number')
  return Number(v)
}
const code: string[] = []
for (const node of parse(source, { modern: true }).instance?.content.body ?? []) {
  if (node.type === 'FunctionDeclaration' && ['shortcut', 'openFind'].includes(node.id?.name ?? '')) code.push(source.slice(offset(node, 'start'), offset(node, 'end')))
}
assert.equal(code.length, 2)

type Fn = (...args: unknown[]) => unknown
const isFn = (v: unknown): v is Fn => typeof v === 'function'

function app(state: Record<string, unknown> = {}) {
  const focused: unknown[] = []
  const views: unknown[] = []
  const c: Record<string, unknown> = { feedShown: true, showPalette: false, drill: null, finding: false, findText: '', GENERAL: 'general', feedFind: { focus: (q: unknown) => focused.push(q) }, openView: (v: unknown) => views.push(v), showSide: true, ...state }
  vm.createContext(c)
  vm.runInContext(stripTypes(code.join('\n')), c)
  return { c, focused, views }
}
// press sends Ctrl+F and says whether the app took the key.
function press(c: Record<string, unknown>, inside = '') {
  let prevented = false
  const target = { closest: (selector: string) => (inside && selector.includes(inside) ? {} : null) }
  const shortcut = c.shortcut
  assert.ok(isFn(shortcut))
  shortcut({ key: 'f', ctrlKey: true, metaKey: false, shiftKey: false, altKey: false, target, preventDefault: () => { prevented = true } })
  return prevented
}

test('Ctrl+F opens find over the room feed, and again focuses the open bar', () => {
  const { c, focused } = app()
  assert.equal(press(c), true)
  assert.equal(c.finding, true)
  assert.deepEqual(focused, [], 'a new bar focuses itself when it mounts')
  press(c)
  assert.deepEqual(focused, [''])
})

test('Ctrl+F stays the browser find in the find box, under a dialog or menu, with the palette open, and off the feed', () => {
  for (const [state, inside] of [[{}, '.find'], [{}, '[role="dialog"]'], [{}, '[role="menu"]'], [{ showPalette: true }, ''], [{ feedShown: false }, '']] as const) {
    const { c } = app(state)
    assert.equal(press(c, inside), false, JSON.stringify([state, inside]))
    assert.equal(c.finding, false)
  }
})

test('find asked from another view, as by /find, opens the feed first', () => {
  const { c, views } = app({ feedShown: false })
  const openFind = c.openFind
  assert.ok(isFn(openFind))
  openFind('deploy')
  assert.deepEqual(views, ['general'])
  assert.equal(c.findText, 'deploy')
  assert.equal(c.finding, true)
})
