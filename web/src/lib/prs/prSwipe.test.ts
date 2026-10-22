import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { neighbor } from './prboard.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import type { Row } from './prlist.ts'

// Run the real PR gesture handlers with controlled touches and rows.
const source = readFileSync(new URL('./GitHub.svelte', import.meta.url), 'utf8')
const nodes = (parse(source, { modern: true }).instance?.content.body ?? []).filter((n) =>
  n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.type === 'Identifier' && ['swipeX', 'swipeStart'].includes(d.id.name)) ||
  n.type === 'FunctionDeclaration' && n.id.name === 'swipeEnd')
assert.equal(nodes.length, 3)

function gestures(selId = 'a') {
  const calls: { id?: string; push: boolean }[] = []
  const c = { neighbor, order: [{ changeId: 'a' }, { changeId: 'b' }], selId, openRow: (row: Row, push: boolean) => calls.push({ id: row.changeId, push }) }
  vm.createContext(c)
  vm.runInContext(nodes.map((n) => { const { start, end } = sourceRange(n); return stripTypes(source.slice(start, end)) }).join('\n'), c)
  const handlers = vm.runInContext('({ swipeStart, swipeEnd })', c)
  return {
    calls,
    swipe(from: number, to: number, inTabs = false) {
      const target = { closest: () => inTabs ? {} : null }
      handlers.swipeStart({ target, changedTouches: [{ clientX: from }] })
      handlers.swipeEnd({ target, changedTouches: [{ clientX: to }] })
    },
  }
}

test('scrolling the PR tab bar does not open a neighboring PR', () => {
  const g = gestures()
  g.swipe(300, 100, true)
  assert.deepEqual(g.calls, [])
})

test('body swipes still open the next and previous PR without adding history', () => {
  const left = gestures()
  left.swipe(300, 100)
  assert.deepEqual(left.calls, [{ id: 'b', push: false }])
  const right = gestures('b')
  right.swipe(100, 300)
  assert.deepEqual(right.calls, [{ id: 'a', push: false }])
})

test('a tab-bar touch clears the previous body gesture', () => {
  const g = gestures()
  g.swipe(300, 100)
  g.swipe(300, 100, true)
  assert.equal(g.calls.length, 1)
})

test('a short body gesture does not change PRs', () => {
  const g = gestures()
  g.swipe(300, 250)
  assert.deepEqual(g.calls, [])
})
