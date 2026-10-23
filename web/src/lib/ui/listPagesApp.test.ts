import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { mergeListPage } from './listPages.ts'
import { reconcileRows } from './reloadGate.ts'
import { topLevelFunctions } from '../../../test/svelte-source.ts'

interface AppRow {
  [field: string]: unknown
}

interface AppPage {
  items: AppRow[]
  older: boolean
  cursor: string
}

// The slice of App applyListPage reads and writes; the vm adds the function.
interface AppListContext {
  listPages: Record<string, { cursor: string; items?: AppRow[]; older?: boolean; loadedOlder?: boolean }>
  mergeListPage: typeof mergeListPage
  reconcileRows: typeof reconcileRows
  applyListPage?: (kind: string, page: AppPage, append?: boolean) => AppRow[]
}

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const code = topLevelFunctions(source, ['applyListPage']).get('applyListPage')
if (code === undefined) throw new Error('App.svelte defines no applyListPage')

for (const [kind, key] of [['jobs', 'id'], ['changes', 'key']] as const) {
  test(`${kind} refresh keeps loaded history, its cursor, and keyed row objects`, () => {
    const c: AppListContext = { listPages: {}, mergeListPage, reconcileRows }
    vm.createContext(c)
    vm.runInContext(code, c)
    const applyListPage = c.applyListPage
    assert.ok(applyListPage !== undefined)
    applyListPage(kind, { items: [{ [key]: 'active', state: 'running' }], older: true, cursor: 'first' })
    const expanded = applyListPage(kind, { items: [{ [key]: 'old', state: 'exited' }], older: true, cursor: 'second' }, true)
    const active = expanded[0], old = expanded[1]
    const incoming = { items: [{ [key]: 'active', state: 'exited' }], older: true, cursor: 'new-first' }
    const refreshed = applyListPage(kind, incoming)
    assert.deepEqual(refreshed.map((row) => row[key]), ['active', 'old'])
    assert.equal(refreshed[0], active)
    assert.equal(refreshed[0]?.state, 'exited')
    assert.equal(refreshed[1], old)
    assert.equal(c.listPages[kind]?.cursor, 'second')
    assert.equal(applyListPage(kind, incoming), refreshed)
  })
}
