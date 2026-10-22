import test from 'node:test'
import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import { setImmediate } from 'node:timers/promises'
import { createRoomData } from './roomData.js'
import { patchListRows, hasItemChange } from '../ui/listChanges.js'
import { reconcileRows } from '../ui/reloadGate.js'
import { reloadsUnread } from '../ui/unreadReload.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const nodes = parse(source).instance.content.body
function app() {
  const calls = [], timers = new Map()
  let sequence = 0
  const c = {
    roomId: 'a', roomRequest: 1, view: 'github', drill: null, feed: null, home: null,
    hierarchy: [{ owner: true, name: 'owner' }, { id: 'one', name: 'one', status: 'running', state: 'live', turns: 1 }],
    members: [], pickedSessions: [], showClosed: false, hasItemChange, reloadsUnread, patchListRows, reconcileRows,
    pruneSelection: () => [], setPicked() {}, channels: { resolveView: (v) => v },
    loadComputers() {}, loadMembers() {}, loadConsolidations() {},
    loadRooms: () => calls.push('rooms'),
    roomSessionController: { load: () => calls.push('controls') },
    setTimeout(fn, delay) { assert.equal(delay, 250); const id = ++sequence; timers.set(id, fn); return id },
    clearTimeout(id) { timers.delete(id) },
  }
  c.routedParents ??= ''; c.feed ??= null
  vm.createContext(c)
  for (const name of ['roomTimer', 'roomRefreshes']) {
    const n = nodes.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === name))
    if (n) vm.runInContext(source.slice(n.start, n.end), c)
  }
  for (const name of ['memberRoster', 'roomCountKey', 'applyMembers', 'applyMemberItem', 'showMemberRows', 'parentKey', 'noteParents', 'applyItemEvent', 'handle', 'soonRooms']) {
    const n = nodes.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    if (n) vm.runInContext(source.slice(n.start, n.end), c)
  }
  const api = { members: async () => [], mentions: async () => [], feedTail: async () => { calls.push('feed'); return {} } }
  c.roomData = createRoomData(api, {
    current: () => ({ room: 'a', visit: 1 }),
    members: (rows) => c.applyMembers(rows),
    patchMembers: (change) => c.applyMemberItem(change), choices() {}, unread() {}, error(error) { throw error },
  })
  return {
    c, calls, api,
    member(patch = {}, flags = {}) {
      const item = { ...c.hierarchy.find((m) => m.id === 'one'), ...patch }
      c.handle({ kind: 'members', roomId: 'a', change: { op: 'upsert', id: 'one', item, ...flags } })
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
  let finish
  a.api.members = () => new Promise((resolve) => { finish = resolve })
  const reading = a.c.roomData.loadMembers()
  const closed = { ...a.c.hierarchy[1], status: 'closed', state: 'closed', closed: true }
  a.member(closed, { controls: true })
  finish([a.c.hierarchy[0], closed])
  await reading
  await a.flush()
  assert.deepEqual(a.calls.toSorted(), ['controls', 'rooms'])
})
