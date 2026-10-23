import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createReloadGate, reconcileRows } from '../ui/reloadGate.ts'
import { createRoomData } from '../rooms/roomData.ts'
import { bulkRetryable } from './bulkSessions.ts'
import { pruneSelection, toggleSelection } from './consolidation.ts'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const names = ['consolidationChanged', 'loadMembers', 'memberRoster', 'roomCountKey', 'showMemberRows', 'parentKey', 'noteParents', 'applyMembers', 'pickSession', 'setPicked', 'bulkChanged', 'toggleSessionSelection', 'closeSelectionMenu']
function deferred<T>() { let resolve: (value: T) => void = () => {}; const promise = new Promise<T>((yes) => { resolve = yes }); return { promise, resolve } }
// The slice of App's state these handlers read and write.
interface Member { id: string; closed?: boolean }
interface Batch { key: number; roomId: string; roomRequest?: number; selectionRevision?: number; action?: string; sessions?: { id: string }[] }
interface App {
  hierarchy: Member[]
  pickedSessions: string[]
  selectingSessions: boolean
  selectionMenu: { anchor: { focus(): void } } | null
  consolidating: Batch | null
  roomRequest: number
  roomId: string
  selectionRevision: number
  consolidationAlive: boolean
  [name: string]: unknown
}
function app(api: Record<string, unknown>): App {
  const c: App = { loadComputers() {}, soonRooms() {}, reconcileRows, api, selectionRevision: 0, roomId: 'a', roomRequest: 1, memberRequest: 0, consolidationAlive: true, consolidating: null, hierarchy: [], pickedSessions: ['one'], members: [], choices: [], showClosed: false, view: 'general', channels: { resolveView: (v: string) => v }, pruneSelection, toggleSelection, bulkRetryable, selectingSessions: false, selectionMenu: null }
  c.reload = createReloadGate(() => ({ room: c.roomId, visit: c.roomRequest }), () => c.consolidationAlive)
  c.routedParents ??= ''; c.feed ??= null
  runFunctions(source, names, c)
  c.roomData = createRoomData(api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }),
    members: (...args: unknown[]) => call(c, 'applyMembers', ...args), choices() {},
  })
  return c
}
test('a stale member list cannot restore a closed selection or replace a newer hierarchy', async () => {
  const old = deferred<Member[]>(), fresh = deferred<Member[]>(); let calls = 0
  const c = app({ members: () => ++calls === 1 ? old.promise : fresh.promise, mentions: async () => [] })
  const first = call(c, 'loadMembers'), second = call(c, 'loadMembers')
  assert.equal(calls, 1)
  fresh.resolve([{ id: 'one', closed: true }])
  old.resolve([{ id: 'one', closed: false }]); await Promise.all([first, second])
  assert.equal(calls, 2)
  assert.equal(need(c.hierarchy[0]).closed, true); assert.equal(c.pickedSessions.length, 0)
})

test('Ctrl/Cmd row activation enters selection and toggles stable IDs', () => {
 const c = app({});c.selectingSessions = false;c.pickedSessions = []
 call(c, 'pickSession', {id:'one'},true,true)
 assert.equal(c.selectingSessions,true);assert.equal(c.pickedSessions[0],'one')
 call(c, 'pickSession', {id:'one'},false)
 assert.equal(c.pickedSessions.length,0)
})

test('batch results retain refusals and cannot change a later selection or room visit', () => {
 const c=app({});c.bulkBatches=[];c.roomRequest=1;c.selectionRevision=0;c.pickedSessions=['one','two'];c.loadChanges=()=>{};c.loadMembers=()=>{}
 const record: Batch={key:1,roomId:'a',roomRequest:1,selectionRevision:0,action:'stop',sessions:[{id:'one'},{id:'two'}]}
 call(c, 'bulkChanged', record,{started:true,busy:true,results:[{session:'one',ok:true},{session:'two',ok:false,error:'refused'}]})
 assert.deepEqual([...c.pickedSessions],['two'])
 call(c, 'setPicked', ['new']);call(c, 'bulkChanged', record,{started:true,busy:false,results:[{session:'one',ok:false},{session:'two',ok:true}]})
 assert.deepEqual([...c.pickedSessions],['new'])
 c.roomRequest=3;call(c, 'bulkChanged', record,{started:true,busy:false,results:[]})
 assert.deepEqual([...c.pickedSessions],['new'])
})

test('Cancel revokes a previous batch selection even when all accepted rows were already removed',()=>{
 const c=app({});c.bulkBatches=[];c.pickedSessions=[];c.selectingSessions=true;c.selectionRevision=0
 const record: Batch={key:1,roomId:'a',roomRequest:1,selectionRevision:0,action:'transfer',sessions:[{id:'one'}]}
 call(c, 'toggleSessionSelection');call(c, 'bulkChanged', record,{started:true,busy:false,results:[{session:'one',ok:true,transfer:{phase:'failed'}}]})
 assert.equal(c.selectingSessions,false);assert.equal(c.pickedSessions.length,0)
})
test('closing the Actions menu restores focus after removing its reactive state',()=>{
 const c=app({});let focus=0;c.selectionMenu={anchor:{focus(){assert.equal(c.selectionMenu,null);focus++}}}
 call(c, 'closeSelectionMenu');assert.equal(focus,1)
})


test('only the current consolidation dialog can clear the member selection', () => {
 const c = app({})
 const current: Batch = { key: 2, roomId: 'a' }
 c.consolidating = current
 c.selectingSessions = true
 call(c, 'consolidationChanged', { key: 1, roomId: 'a' }, { id: 'old', roomId: 'a' })
 assert.equal(c.selectingSessions, true)
 call(c, 'consolidationChanged', current, { id: 'other-room', roomId: 'b' })
 assert.equal(c.selectingSessions, true)
 call(c, 'consolidationChanged', current, { id: 'active', roomId: 'a' })
 assert.equal(c.selectingSessions, false)
 assert.equal(c.pickedSessions.length, 0)
})
