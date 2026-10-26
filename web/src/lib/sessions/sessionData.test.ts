import test from 'node:test'
import assert from 'node:assert/strict'
import { createSessionData, type PageDirection, type SessionApi, type SessionHooks, type SessionSelection } from './sessionData.ts'
import { mergeSessionPage } from './sessionPages.ts'
import { post, session, turn } from '../../../test/session-fixtures.ts'
import type { Session } from '../api/session.ts'

// A test names only the calls and hooks it uses; the rest do nothing.
const api = (calls: Partial<SessionApi>): SessionApi => ({
  sessionQueue: async () => [],
  rootSession: async (room) => session(room),
  transcriptTurn: async (id) => session(id),
  transcriptPage: async (id) => session(id),
  transcriptAfter: async (id) => session(id),
  ...calls,
})
const selection = (extra: Partial<SessionSelection> = {}): SessionSelection => ({ room: 'room', visit: 1, id: 'session', selection: '1', turn: 0, root: false, live: false, ...extra })
const hooks = (extra: Partial<SessionHooks> = {}): SessionHooks => ({ current: () => selection(), queue() {}, transcript() {}, error() {}, ...extra })

test('DM and thread page directions serialize and present fetches a fresh tail', async () => {
 let finish: () => void = () => {}
 const wait=new Promise<void>(r=>{finish=r})
 const calls: string[][]=[]
 const applied: PageDirection[]=[]
 let active=0,max=0
 const fetch=async (kind: string,id: string,cursor: string)=>{
  calls.push([kind,id,cursor]);max=Math.max(max,++active)
  if(kind==='older' && cursor==='20') await wait
  active--;return session(id)
 }
 const data=createSessionData(api({
  transcriptPage:(id,cursor)=>fetch('older',id,cursor),
  transcriptAfter:(id,cursor)=>fetch('newer',id,cursor),
 }),hooks({transcript:(_,direction)=>{applied.push(direction ?? false)}}))
 const older=data.earlier('20')
 const newer=data.newer('25')
 await Promise.resolve()
 assert.deepEqual(calls,[['older','session','20']])
 finish()
 await Promise.all([older,newer])
 await data.present()
 assert.equal(max,1)
 assert.deepEqual(calls,[['older','session','20'],['newer','session','24'],['older','session','']])
 assert.deepEqual(applied,[true,'newer','present'])
 data.destroy()
})


test('forward paging restores withheld posts from the retained final turn before clearing newer', async () => {
 const initial=session('session',{turns:[turn(1),turn(2)],posts:[post('old')],newer:false})
 const tail=session('session',{turns:[turn(2)],posts:[post('old'),post('during-turn')],newer:false})
 let current: Session=mergeSessionPage(initial,tail,false,true)
 assert.deepEqual((current.posts ?? []).map(p=>p.id),['old'])
 assert.equal(current.newer,true)
 const calls: string[]=[]
 const data=createSessionData(api({
  transcriptAfter:async (id,cursor)=>{
   calls.push(cursor)
   return cursor==='1' ? tail : session(id)
  },
 }),hooks({transcript:(page,direction)=>{current=mergeSessionPage(current,page,direction,true)}}))
 await data.newer('2')
 assert.deepEqual((current.posts ?? []).map(p=>p.id),['old','during-turn'])
 assert.equal(current.newer,false)
 assert.deepEqual(calls,['1'])
 data.destroy()
})

test('the root conversation loads through RootSession, with no queue and no paging', async () => {
 const calls: string[][] = [], applied: Session[] = []
 const data = createSessionData(api({
  rootSession: async (room) => { calls.push(['root', room]); return session('root') },
  sessionQueue: async () => { calls.push(['queue']); return [] },
  transcriptPage: async () => { calls.push(['page']); return session('page') },
 }), hooks({ current: () => selection({ id: 'root:room', root: true }), transcript: (value) => { applied.push(value) } }))
 await data.load()
 assert.deepEqual(calls, [['root', 'room']])
 assert.equal(applied.length, 1)
 assert.equal(await data.earlier('3'), false)
 assert.deepEqual(calls, [['root', 'room']])
 data.destroy()
})

test('a root post that arrived while reading history is revealed by present and newer', async () => {
 const applied: [string | undefined, PageDirection | undefined][] = []
 const data = createSessionData(api({ rootSession: async () => session('root', { posts: [post('new')] }) }),
  hooks({ current: () => selection({ id: 'root:room', root: true }), transcript: (value, direction) => { applied.push([value.posts?.[0]?.id, direction]) } }))
 assert.equal(await data.present(), true)
 assert.equal(await data.newer(''), true)
 assert.deepEqual(applied, [['new', 'present'], ['new', 'present']])
 data.destroy()
})

// The App loader for a transcript page only forwards the near-edge check to
// this controller, so the controller is driven here. Slicing the App module to
// reach the loader loaded the App module graph and the TypeScript compiler into
// the test, which is what made it slow.
test('a delayed history page is discarded after the reader leaves the edge', async () => {
  for (const direction of ['earlier', 'newer'] as const) {
    let finish: (page: Session) => void = () => {}
    const pending = new Promise<Session>((resolve) => { finish = resolve })
    const applied: Session[] = []
    const data = createSessionData(api({ transcriptPage: () => pending, transcriptAfter: () => pending }), hooks({ transcript: (page) => applied.push(page) }))
    let nearEdge = true
    const loading = direction === 'earlier' ? data.earlier('2', () => nearEdge) : data.newer('2', () => nearEdge)
    nearEdge = false
    finish(session('session', { posts: [post('late')] }))
    await loading
    assert.deepEqual(applied, [], direction + ' must keep the viewport after the reader moves away')
    data.destroy()
  }
})
