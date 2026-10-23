import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { changesVisuals } from '../files/visuals.ts'
import { createReloadGate, listVisible } from '../ui/reloadGate.ts'
import { hasItemChange } from '../ui/listChanges.ts'
import { createRoomData } from './roomData.js'
import { createRoomSessionControls } from './roomSessionControls.js'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const functions = parse(source).instance.content.body
function app(view) {
  const calls = []
  const c = {
    hasItemChange, reloadsUnread, view, mergeVersion: 0, roomId: 'a', roomRequest: 1, drill: null, home: null, feed: null,
    memberChips: true, consolidationAlive: true,
    roomControls: null, loadQuestions() {}, loadVisuals() {}, soonRooms: (kind = 'rooms') => kind === 'controls' ? c.roomSessionController.load() : calls.push(kind),
    sessionData: { load: async () => calls.push('transcript') },
    changesTracker: { poke: () => calls.push('changes') },
    api: {
      members: async () => { calls.push('members'); return [] },
      sessionConsolidations: async () => { calls.push('consolidations'); return [] },
      roomSessionControls: async () => { calls.push('controls'); return { roomId: 'a', paused: false, running: 0, held: 0, dormant: 0 } },
    },
  }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), (name) => listVisible(name, c))
  c.roomData = createRoomData(c.api, { current: () => ({ room: c.roomId, visit: c.roomRequest }), members() {}, choices() {} })
  c.roomSessionController = createRoomSessionControls(c.api, 'a', (value) => { c.roomControls = value }, () => listVisible('roomControls', c))
  c.changesVisuals = changesVisuals
  vm.createContext(c)
  for (const name of ['applyItemEvent', 'handle', 'loadSession', 'loadMembers']) {
    const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    vm.runInContext(stripTypes(source.slice(node.start, node.end)), c)
  }
  return { c, calls, close() { c.roomData.destroy(); c.roomSessionController.destroy() } }
}

for (const view of ['github', 'channel:general']) {
  test(`${view}: streaming output skips room lists and only reloads the selected session`, async () => {
    const { c, calls, close } = app(view)
    c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'other' } })
    await new Promise(setImmediate)
    assert.deepEqual(calls, [])
    c.drill = { id: 'one' }
    c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'other' } })
    c.handle({ kind: 'session', roomId: 'b', change: { op: 'upsert', id: 'one' } })
    assert.deepEqual(calls, [])
    c.handle({ kind: 'session', roomId: 'a', change: { op: 'upsert', id: 'one' } })
    await new Promise(setImmediate)
    // Activity in the open session also asks for a refresh of its Changes tab count.
    assert.deepEqual(calls.sort(), ['changes', 'transcript'])
    close()
  })
}

test('member metadata refreshes controls only when their flags change', async () => {
  const { c, calls, close } = app('github')
  c.handle({ kind: 'members', roomId: 'a', change: { op: 'upsert', id: 'one' } })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['feed', 'members', 'rooms'])
  calls.length = 0
  c.handle({ kind: 'members', roomId: 'a', change: { op: 'upsert', id: 'one', controls: true, consolidations: true } })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['feed', 'members', 'controls', 'rooms'])
  close()
})

test('reconnect refreshes visible controls without consolidation history', async () => {
  const { c, calls, close } = app('github')
  c.loadRooms = () => calls.push('rooms')
  c.roomData.open = async () => true
  c.openRun = ''
  for (const name of ['loadChanges', 'loadGoals', 'loadRules', 'loadVisuals']) c[name] = () => {}
  let reviewerLoads = 0
  c.loadReviewer = () => reviewerLoads++
  const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'resync')
  vm.runInContext(stripTypes(source.slice(node.start, node.end)), c)
  await c.resync()
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['rooms', 'controls'])
  // Reviewer settings that changed while the stream was down show again.
  assert.equal(reviewerLoads, 1)
  assert.equal(c.mergeVersion, 1)
  close()
})

test('reconnect refreshes the open session and its Changes count', async () => {
  const { c, calls, close } = app('github')
  c.loadRooms = () => {}
  c.roomData.open = async () => true
  c.openRun = ''
  for (const name of ['loadChanges', 'loadGoals', 'loadRules', 'loadVisuals', 'loadReviewer']) c[name] = () => {}
  c.drill = { id: 'one' }
  const node = functions.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'resync')
  vm.runInContext(stripTypes(source.slice(node.start, node.end)), c)
  await c.resync()
  await new Promise(setImmediate)
  assert.ok(calls.includes('transcript') && calls.includes('changes'), `calls: ${calls}`)
  close()
})

test('the remaining timer refreshes bulk transfers only while the page is visible', () => {
  const calls = []
  const c = { needPair: false, pageVisible: true, bulkControllers: new Map([['one', { refreshTransfers: () => calls.push('transfer') }]]) }
  c.changesVisuals = changesVisuals
  vm.createContext(c)
  const mounted = functions.find((n) => n.type === 'ExpressionStatement' && n.expression.callee?.name === 'onMount')
  const binding = mounted.expression.arguments[0].body.body.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'transferPoll'))
  const callback = binding.declarations.find((d) => d.id.name === 'transferPoll').init.arguments[0]
  const poll = vm.runInContext(stripTypes(source.slice(callback.start, callback.end)), c)
  poll()
  assert.deepEqual(calls, ['transfer'])
  calls.length = 0
  c.pageVisible = false
  poll()
  assert.deepEqual(calls, [])
})

test('room summary changes skip controls, while explicit pause changes refresh them', async () => {
  const { c, calls, close } = app('github')
  c.handle({ kind: 'rooms' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['rooms'])
  calls.length = 0
  c.handle({ kind: 'room.paused', roomId: 'a' })
  await new Promise(setImmediate)
  assert.deepEqual(calls, ['controls', 'rooms'])
  close()
})

for (const scenario of [
  { name: 'paused visible room', paused: true, visible: true, room: 'a', expected: 1 },
  { name: 'unpaused room', paused: false, visible: true, room: 'a', expected: 0 },
  { name: 'hidden controls', paused: true, visible: false, room: 'a', expected: 0 },
  { name: 'another room', paused: true, visible: true, room: 'other', expected: 0 },
]) test(`${scenario.name}: pending posts refresh held counts only when relevant`, async () => {
  const { c, calls, close } = app('github')
  let held = 0
  c.api.roomSessionControls = async () => {
    calls.push('controls')
    return { roomId: 'a', paused: scenario.paused, running: 0, held, dormant: 0 }
  }
  await c.roomSessionController.load()
  assert.equal(c.roomControls.state.held, 0)
  calls.length = 0
  c.memberChips = scenario.visible
  held = 1
  c.handle({ kind: 'posts', roomId: scenario.room, posts: [{ id: 'new', from: 'a', to: ['a:one'], text: 'pending work' }] })
  await new Promise(setImmediate)
  assert.equal(calls.filter((name) => name === 'controls').length, scenario.expected)
  assert.equal(c.roomControls.state.held, scenario.expected)
  close()
})

for (const cached of [false, true]) test(`a post during ${cached ? 'an unpaused-state' : 'the initial'} control load queues a trailing held-count refresh`, async () => {
  const { c, close } = app('github')
  if (cached) await c.roomSessionController.load()
  let finish, calls = 0
  const first = new Promise((resolve) => { finish = resolve })
  c.api.roomSessionControls = () => ++calls === 1 ? first : Promise.resolve({ roomId: 'a', paused: true, running: 0, held: 1, dormant: 0 })
  const pending = c.roomSessionController.load()
  c.handle({ kind: 'posts', roomId: 'a', posts: [{ id: 'new', to: ['a:one'] }] })
  finish({ roomId: 'a', paused: true, running: 0, held: 0, dormant: 0 })
  await pending
  assert.equal(calls, 2)
  assert.equal(c.roomControls.state.held, 1)
  close()
})

test('merge refresh version follows room PR and change events only', () => {
  const { c, close } = app('github')
  c.refreshHeldComments = () => {}
  c.reviewerSoon = () => {}
  c.loadChanges = () => {}
  c.handle({ kind: 'prs', roomId: 'b' })
  assert.equal(c.mergeVersion, 0)
  c.handle({ kind: 'prs', roomId: 'a' })
  c.handle({ kind: 'changes', roomId: 'a' })
  assert.equal(c.mergeVersion, 2)
  close()
})

test('a jobs metadata event reloads jobs only for the open room', () => {
  const { c, calls, close } = app('jobs')
  c.workflowData = { loadJobs: () => calls.push('jobs') }
  c.handle({ kind: 'jobs', roomId: 'other' })
  assert.deepEqual(calls, [])
  c.handle({ kind: 'jobs', roomId: 'a' })
  assert.deepEqual(calls, ['members', 'jobs'])
  close()
})
