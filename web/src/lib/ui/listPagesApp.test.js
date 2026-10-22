import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { mergeListPage } from './listPages.js'
import { reconcileRows } from './reloadGate.js'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const node = parse(source).instance.content.body.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'applyListPage')
const code = source.slice(node.start, node.end)

for (const [kind, key] of [['jobs', 'id'], ['changes', 'key']]) {
  test(`${kind} refresh keeps loaded history, its cursor, and keyed row objects`, () => {
    const c = { listPages: {}, mergeListPage, reconcileRows }
    vm.createContext(c)
    vm.runInContext(code, c)
    c.applyListPage(kind, { items: [{ [key]: 'active', state: 'running' }], older: true, cursor: 'first' })
    const expanded = c.applyListPage(kind, { items: [{ [key]: 'old', state: 'exited' }], older: true, cursor: 'second' }, true)
    const active = expanded[0], old = expanded[1]
    const incoming = { items: [{ [key]: 'active', state: 'exited' }], older: true, cursor: 'new-first' }
    const refreshed = c.applyListPage(kind, incoming)
    assert.deepEqual(refreshed.map((row) => row[key]), ['active', 'old'])
    assert.equal(refreshed[0], active)
    assert.equal(refreshed[0].state, 'exited')
    assert.equal(refreshed[1], old)
    assert.equal(c.listPages[kind].cursor, 'second')
    assert.equal(c.applyListPage(kind, incoming), refreshed)
  })
}
