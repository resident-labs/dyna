import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const body = parse(source, { modern: true }).instance?.content.body ?? []
const fn = (name: string) => {
  const node = body.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === name)
  if (!node) throw new Error(`App has no ${name}`)
  return sourceRange(node)
}
function app(names: readonly string[], c: object): void {
  vm.createContext(c)
  for (const n of names) {
    const { start, end } = fn(n)
    vm.runInContext(stripTypes(source.slice(start, end)), c)
  }
}
// vm runs untyped JavaScript, so each test names the App functions it calls.
function functionsOf<T>(c: object, names: readonly string[]): T {
  return vm.runInContext(`({ ${names.join(', ')} })`, c) as T
}

interface IssueRow {
  id: string
  state: string
  changedAt: number
}

interface IssuesState {
  roomId: string
  issues: IssueRow[]
  issueHistory: IssueRow[]
  showIssues: boolean
  loadError: string
  reconcileRows: typeof reconcileRows
  reload?: ReturnType<typeof createReloadGate>
  api: { issues: () => Promise<IssueRow[]> }
}

test('opening a room loads its issues, so a first open and a reconnect both show them', () => {
  const applyRoomOpened = fn('applyRoomOpened')
  assert.match(source.slice(applyRoomOpened.start, applyRoomOpened.end), /loadIssues\(\)/)
})

test('the History list drops open issues that the all=true call returns', async () => {
  const c: IssuesState = {
    roomId: 'a', issues: [], issueHistory: [], showIssues: true, loadError: '', reconcileRows,
    api: { issues: async () => [{ id: 'o', state: 'open', changedAt: 0 }, { id: 'c', state: 'closed', changedAt: 5 }, { id: 'g', state: 'goal', changedAt: 6 }] },
  }
  c.reload = createReloadGate(() => ({ room: 'a', visit: 1 }), () => true)
  app(['loadIssues'], c)
  const { loadIssues } = functionsOf<{ loadIssues: (all?: boolean) => Promise<void> }>(c, ['loadIssues'])
  await loadIssues(true)
  assert.deepEqual(c.issueHistory.map((i) => i.id), ['c', 'g'])
})

test('Close asks for a reason and sends it', async () => {
  const calls: unknown[][] = []
  interface Ask {
    action: string
    label: string
    submit: (reason: string) => Promise<void>
  }
  const c = {
    roomId: 'a', ask: null as Ask | null, loadError: '', showIssues: false, say() {},
    api: { closeIssue: async (...a: unknown[]) => { calls.push(a) } },
    loadIssues() {},
  }
  app(['triageIssue', 'askCloseIssue'], c)
  const { askCloseIssue } = functionsOf<{ askCloseIssue: (issue: { id: string; title: string }) => void }>(c, ['askCloseIssue'])
  askCloseIssue({ id: 'i1', title: 'Pin menu loses focus' })
  assert.equal(c.ask?.action, 'Close')
  assert.equal(c.ask?.label, 'Reason')
  await c.ask?.submit('duplicate of i0')
  assert.deepEqual(calls, [['a', 'i1', 'duplicate of i0']])
  assert.equal(c.ask, null)
})
