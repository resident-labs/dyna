import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import { createReloadGate } from '../ui/reloadGate.ts'
import { sourceRange } from '../../../test/svelte-source.ts'
import { installIssues } from '../../../test/appModules.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const body = parse(source, { modern: true }).instance?.content.body ?? []
const fn = (name: string) => {
  const node = body.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === name)
  if (!node) throw new Error(`App has no ${name}`)
  return sourceRange(node)
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
  reload?: ReturnType<typeof createReloadGate>
  api: { issues?: () => Promise<IssueRow[]>; closeIssue?: (...args: unknown[]) => Promise<unknown> }
  [name: string]: unknown
}

// The issue lists live in lib/app/issues.svelte.ts now, so a test installs that
// module over its own state, as appRoomLoader.ts installs the room data.
function app(c: IssuesState): IssuesState {
  installIssues(c)
  return c
}

test('opening a room loads its issues, so a first open and a reconnect both show them', () => {
  const applyRoomOpened = fn('applyRoomOpened')
  assert.match(source.slice(applyRoomOpened.start, applyRoomOpened.end), /loadIssues\(\)/)
})

test('the History list drops open issues that the all=true call returns', async () => {
  const c = app({
    roomId: 'a', issues: [], issueHistory: [], showIssues: true, loadError: '',
    api: { issues: async () => [{ id: 'o', state: 'open', changedAt: 0 }, { id: 'c', state: 'closed', changedAt: 5 }, { id: 'g', state: 'goal', changedAt: 6 }] },
  })
  c.reload = createReloadGate(() => ({ room: 'a', visit: 1 }), () => true)
  await (c.loadIssues as (all?: boolean) => Promise<void>)(true)
  assert.deepEqual(c.issueHistory.map((i) => i.id), ['c', 'g'])
})

test('Close asks for a reason and sends it', async () => {
  const calls: unknown[][] = []
  interface Ask {
    action: string
    label: string
    submit: (reason: string) => Promise<void>
  }
  const c = app({
    roomId: 'a', issues: [], issueHistory: [], ask: null as Ask | null, loadError: '', showIssues: false, say() {},
    api: { closeIssue: async (...a: unknown[]) => { calls.push(a) } },
    loadIssues() {},
  })
  ;(c.askCloseIssue as (issue: { id: string; title: string }) => void)({ id: 'i1', title: 'Pin menu loses focus' })
  const ask = c.ask as Ask | null
  assert.equal(ask?.action, 'Close')
  assert.equal(ask?.label, 'Reason')
  await ask?.submit('duplicate of i0')
  assert.deepEqual(calls, [['a', 'i1', 'duplicate of i0']])
})
