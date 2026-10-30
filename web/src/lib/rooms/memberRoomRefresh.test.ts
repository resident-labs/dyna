import test from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import { setImmediate } from 'node:timers/promises'
import { patchListRows, hasItemChange } from '../ui/listChanges.ts'
import { reconcileRows } from '../ui/reloadGate.ts'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { installRoomLoader } from '../../../test/appRoomLoader.ts'
import type { Member } from '../api/room.ts'
import type { ListChange } from '../api/event.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const nodes = parse(source, { modern: true }).instance?.content.body ?? []

// The controller keys a roster by name, owner flag, state and turns; the rows
// carry the other Member fields with their empty values.
const row = (patch: Partial<Member> & { name: string }): Member => ({
  resumable: false, address: '', depth: 0, children: 0, status: '', state: '', turns: 0, created: '', lastActivity: '', hue: 0, ...patch,
})

interface MemberChange {
  op: string
  id?: string
  item?: unknown
  controls?: boolean
}

interface AppEvent {
  kind: string
  roomId?: string
  change?: MemberChange
}

interface MembersState {
  roomId: string
  roomRequest: number
  view: string
  drill: null
  feed: { posts?: { id: string; unread?: boolean }[] } | null
  home: null
  hierarchy: [Member, Member]
  members: Member[]
  pickedSessions: string[]
  showClosed: boolean
  hasItemChange: typeof hasItemChange
  reloadsUnread: typeof reloadsUnread
  patchListRows: typeof patchListRows
  reconcileRows: typeof reconcileRows
  pruneSelection: () => string[]
  setPicked: () => void
  feedViews: { resolveView: (view: string) => string }
  loadComputers: () => void
  loadMembers: () => void
  loadConsolidations: () => void
  loadRooms: () => void
  roomSessionController: { load: () => void }
  setTimeout: (fn: () => void, delay: number) => number
  clearTimeout: (id: number) => void
  routedParents?: string
  roomData?: { loadMembers(): Promise<unknown> }
}

interface MembersFns {
  handle: (event: AppEvent) => void
  applyMembers: (rows: Member[]) => void
  applyMemberItem: (change: ListChange) => void
}

interface MembersApp extends MembersState, MembersFns {
  api: { members: () => Promise<Member[]>; mentions: () => Promise<unknown[]>; feedTail: () => Promise<Record<string, unknown>> }
}

function app() {
  const calls: string[] = []
  const timers = new Map<number, () => void>()
  let sequence = 0
  const c: MembersState = {
    roomId: 'a', roomRequest: 1, view: 'github', drill: null, feed: null, home: null,
    hierarchy: [row({ owner: true, name: 'owner' }), row({ id: 'one', name: 'one', status: 'running', state: 'live', turns: 1 })],
    members: [], pickedSessions: [], showClosed: false, hasItemChange, reloadsUnread, patchListRows, reconcileRows,
    pruneSelection: () => [], setPicked() {}, feedViews: { resolveView: (v: string) => v },
    loadComputers() {}, loadMembers() {}, loadConsolidations() {},
    loadRooms: () => calls.push('rooms'),
    roomSessionController: { load: () => calls.push('controls') },
    setTimeout(fn: () => void, delay: number) { assert.equal(delay, 250); const id = ++sequence; timers.set(id, fn); return id },
    clearTimeout(id: number) { timers.delete(id) },
  }
  c.routedParents ??= ''; c.feed ??= null
  // The screen state lives in the router now, so the sliced functions write through it.
  installRouterShim(c)
  Object.assign(c, { controls: c.roomSessionController, roomControls: null })
  vm.createContext(c)
  for (const name of ['roomTimer', 'roomRefreshes']) {
    const n = nodes.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.type === 'Identifier' && d.id.name === name))
    if (n) {
      const { start, end } = sourceRange(n)
      vm.runInContext(stripTypes(source.slice(start, end)), c)
    }
  }
  const api = { members: async () => [] as Member[], mentions: async () => [] as unknown[], feedTail: async () => { calls.push('feed'); return {} } }
  // The member rows and the room data they feed live in the room loader.
  installRoomLoader(c, api)
  for (const name of ['applyItemEvent', 'handle', 'soonRooms']) {
    const n = nodes.find((n) => n.type === 'FunctionDeclaration' && n.id?.name === name)
    if (n) {
      const { start, end } = sourceRange(n)
      vm.runInContext(stripTypes(source.slice(start, end)), c)
    }
  }
  const fns = vm.runInContext('({ handle, applyMembers, applyMemberItem })', c) as MembersFns
  // The App functions read the room data, so the shared state is the app.
  const state: MembersApp = Object.assign(c, fns, { api })
  return {
    c: state, calls, api,
    member(patch: Partial<Member> = {}, flags: { controls?: boolean } = {}) {
      const item = { ...c.hierarchy.find((m) => m.id === 'one'), ...patch }
      state.handle({ kind: 'members', roomId: 'a', change: { op: 'upsert', id: 'one', item, ...flags } })
    },
    async flush() { await setImmediate(); for (const fn of [...timers.values()]) fn(); timers.clear(); await setImmediate() },
  }
}

test('a running member turn update refreshes controls without reloading all rooms', async () => {
  const a = app()
  a.member({ turns: 2 }, { controls: true })
  await a.flush()
  assert.deepEqual(a.calls.toSorted(), ['controls'])
})

test('a burst of member control updates shares one scheduled control read', async () => {
  const a = app()
  for (let turns = 2; turns <= 9; turns++) a.member({ turns }, { controls: true })
  await a.flush()
  assert.deepEqual(a.calls.toSorted(), ['controls'])
})

test('a real live or open membership change still refreshes room counts', async () => {
  for (const patch of [
    { status: 'idle', state: 'idle' },
    { status: 'closed', state: 'closed', closed: true },
  ]) {
    const a = app()
    a.member(patch, { controls: true })
    await a.flush()
    assert.deepEqual(a.calls.toSorted(), ['controls', 'rooms'])
  }
})

test('room events and current-member changes share the room read schedule', async () => {
  const a = app()
  a.member({ state: 'idle', status: 'idle' }, { controls: true })
  a.c.handle({ kind: 'rooms' })
  await a.flush()
  assert.deepEqual(a.calls.toSorted(), ['controls', 'rooms'])
})

test('unloaded member changes in another room still refresh the room list', async () => {
  const a = app()
  a.c.handle({ kind: 'members', roomId: 'b', change: { op: 'delete', id: 'other' } })
  await a.flush()
  assert.deepEqual(a.calls, ['rooms'])
})

test('an unknown current-room member delete conservatively refreshes counts', async () => {
  const a = app()
  a.c.handle({ kind: 'members', roomId: 'a', change: { op: 'delete', id: 'unknown' } })
  await a.flush()
  assert.deepEqual(a.calls, ['rooms'])
})

test('a full member response cannot consume the count change before its queued item patch', async () => {
  const a = app()
  let finish: (rows: Member[]) => void = () => { throw new Error('no pending members request') }
  a.api.members = () => new Promise((resolve) => { finish = resolve })
  const reading = a.c.roomData?.loadMembers()
  const closed = { ...a.c.hierarchy[1], status: 'closed', state: 'closed', closed: true }
  a.member(closed, { controls: true })
  finish([a.c.hierarchy[0], closed])
  await reading
  await a.flush()
  assert.deepEqual(a.calls.toSorted(), ['controls', 'rooms'])
})
