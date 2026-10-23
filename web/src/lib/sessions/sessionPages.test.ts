import test from 'node:test'
import assert from 'node:assert/strict'
import { mergeSessionPage, type PagePost, type PageTurn, type SessionPage } from './sessionPages.ts'
import { need } from '../../../test/need.ts'

interface TestTurn extends PageTurn {
  status?: string
  started?: string
}
interface TestPost extends PagePost {
  text?: string
}
interface TestPage extends SessionPage {
  turns: TestTurn[]
  posts: TestPost[]
}
const page = (numbers: number[], extra: Partial<TestPage> = {}): TestPage => ({ member: { id: 'session' }, turns: numbers.map(n => ({ n })), posts: numbers.map(n => ({ id: `p${n}`, text: `${n}` })), ...extra })

test('older session pages add turns and posts without replacing live state', () => {
  const current = page([4, 5, 6], { earlier: true, cursor: '4' })
  const old = page([1, 2, 3], { earlier: false, cursor: '1' })
  const result = mergeSessionPage(current, old, true)
  assert.deepEqual(result.turns.map(t => t.n), [1, 2, 3, 4, 5, 6])
  assert.deepEqual(result.posts.map(p => p.id), ['p1', 'p2', 'p3', 'p4', 'p5', 'p6'])
  assert.equal(result.turns[5], current.turns[2])
  assert.equal(result.earlier, false)
  assert.equal(result.cursor, '1')
})

test('older session pages merge reference rows by post', () => {
  const current = page([4, 5, 6], { references: [{ postId: 'r6' }] })
  const old = page([1, 2, 3], { references: [{ postId: 'r1' }] })
  const result = mergeSessionPage(current, old, true)
  assert.deepEqual(result.references?.map(r => r.postId), ['r1', 'r6'])
})

test('a refreshed tail updates live turns while keeping earlier history', () => {
  const current = page([1, 2, 3, 4], { earlier: false, cursor: '1' })
  const fresh = page([3, 4, 5], { earlier: true, cursor: '3' })
  need(fresh.turns[1]).status = 'ok'
  need(fresh.posts[1]).text = 'updated'
  const result = mergeSessionPage(current, fresh)
  assert.deepEqual(result.turns.map(t => t.n), [1, 2, 3, 4, 5])
  assert.deepEqual(result.posts.map(p => p.id), ['p1', 'p2', 'p3', 'p4', 'p5'])
  assert.equal(need(result.turns[3]).status, 'ok')
  assert.equal(need(result.posts[3]).text, 'updated')
  assert.equal(result.earlier, false)
  assert.equal(result.cursor, '1')
})

test('a different session starts a new page window', () => {
  const next = { ...page([1]), member: { id: 'other' } }
  assert.equal(mergeSessionPage(page([4, 5]), next), next)
  assert.equal(mergeSessionPage(null, next), next)
})

test('old DM and thread windows ignore a disjoint tail until forward paging or present', () => {
 const old=page([1,2],{earlier:false,newer:true,cursor:'1'})
 assert.deepEqual(mergeSessionPage(old,page([10,11])).turns.map(t=>t.n),[1,2])
 const next=mergeSessionPage(old,page([3,4],{newer:true}),'newer')
 assert.deepEqual(next.turns.map(t=>t.n),[1,2,3,4])
 assert.equal(next.newer,true)
 assert.deepEqual(mergeSessionPage(next,page([10,11]),'present').turns.map(t=>t.n),[10,11])
})

test('session pages bound turns and posts and can re-fetch both directions', () => {
 const dated = (start: number,count: number,extra: Partial<TestPage>={}): TestPage => ({
  member:{id:'session'}, ...extra,
  turns:Array.from({length:count},(_,i)=>({n:start+i,started:new Date((start+i)*1000).toISOString(),ended:new Date((start+i)*1000+900).toISOString()})),
  posts:Array.from({length:count},(_,i)=>({id:'p'+(start+i),ts:(start+i)*1000+500})),
 })
 const current=dated(21,20,{earlier:true,cursor:'21'})
 const old=mergeSessionPage(current,dated(1,20,{earlier:false,cursor:'1'}),true)
 assert.equal(old.turns.length,18)
 assert.equal(old.posts.length,18)
 assert.equal(need(old.turns[0]).n,1)
 assert.equal(old.newer,true)
 const forward=mergeSessionPage(old,dated(19,6,{newer:true}),'newer')
 assert.equal(need(forward.turns.at(-1)).n,24)
})

test('a reader above present keeps the visible DM window when a new turn arrives', () => {
 const current=page([1,2],{earlier:false,newer:false})
 const merged=mergeSessionPage(current,page([2,3]),false,true)
 assert.deepEqual(merged.turns.map(t=>t.n),[1,2])
 assert.deepEqual(merged.posts.map(p=>p.id),['p1','p2'])
 assert.equal(merged.newer,true)
})
