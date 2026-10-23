import test from 'node:test'
import assert from 'node:assert/strict'
import { createBulkSessionAction, bulkResultText, adoptTargets, type BulkApi, type BulkContext, type BulkOptions, type BulkValue } from './bulkSessions.ts'
import type { BulkSessionResult, TransferState } from '../api/session.ts'
import { member } from '../../../test/session-fixtures.ts'
import { need } from '../../../test/need.ts'
function deferred<T = never>(){let resolve: (value: T) => void=()=>{},reject: (error: Error) => void=()=>{};const promise=new Promise<T>((a,b)=>{resolve=a;reject=b});return{promise,resolve,reject}}
const sessionsOf = (opts: BulkOptions) => opts.sessions ?? []
// accepted answers a request with every session accepted.
const accepted = (opts: BulkOptions, ok = true): BulkSessionResult => ({ action: opts.action, results: sessionsOf(opts).map(session=>({session,ok})), succeeded: 0, failed: 0 })
const transfer = (id: string, phase: string): TransferState => ({ id, session: 's0', phase, profile: 'model', previousProfile: 'old', previousDir: '', previousResumable: false, oldTurns: 0, requested: '', deadline: '', postId: '' })
function setup(n: number,overrides: Partial<BulkApi>={},extra: Partial<BulkContext>={}){
 let value: BulkValue | undefined;const calls: BulkOptions[]=[];const sessions=Array.from({length:n},(_,i)=>({id:`s${i}`,name:`agent ${i}`}))
 const api: BulkApi={settings:async()=>[],profiles:async()=>[{name:'model',harness:'h'}],sessionTransfer:async()=>null,bulkSessionAction:async(_room,opts)=>{calls.push(opts);return accepted(opts)},...overrides}
 const controller=createBulkSessionAction(api,{roomId:'room',sessions,action:'stop',...extra},next=>{value=next})
 return{controller,calls,get value(){return need(value)}}
}
test('large selections use 512-session chunks and keep successful earlier results after transport failure',async()=>{
 const calls: BulkOptions[]=[];const c=setup(1100,{bulkSessionAction:async(_room,opts)=>{calls.push(opts);if(calls.length===2)throw new Error('connection lost');return accepted(opts)}})
 await c.controller.run();assert.deepEqual(calls.map(c=>sessionsOf(c).length),[512,512]);assert.equal(c.value.results.length,1100)
 assert.equal(c.value.results.filter(r=>r.ok).length,512);assert.match(need(c.value.results[512]).error ?? '',/not confirmed/);assert.match(need(c.value.results[1099]).error ?? '',/Not submitted/)
})
test('retry submits only refusals and retains accepted rows',async()=>{
 let count=0;const c=setup(3,{bulkSessionAction:async(_room,opts)=>{c.calls.push(opts);return{action:opts.action,results:sessionsOf(opts).map(session=>({session,ok:++count!==2,error:'refused'})),succeeded:0,failed:0}}})
 await c.controller.run();assert.equal(c.value.results.filter(r=>r.ok).length,2)
 await c.controller.run();assert.deepEqual(need(c.calls[1]).sessions,['s1']);assert.equal(c.value.results.filter(r=>r.ok).length,3)
})
test('one model is chosen explicitly and a transfer acceptance is never labeled completed',async()=>{
 const c=setup(2,{}, {action:'transfer'});await c.controller.load();await c.controller.run();assert.equal(c.calls.length,0)
 c.controller.configure({profile:'model'});await c.controller.run();assert.equal(need(c.calls[0]).profile,'model')
 assert.match(bulkResultText({ok:true,transfer:{phase:'waiting'}},'transfer'),/accepted/)
 assert.equal(bulkResultText({ok:true,transfer:{phase:'completed'}},'transfer'),'New model ready')
})
test('force is absent by default and appears only on the explicit forced retry',async()=>{
 const c=setup(2,{bulkSessionAction:async(_room,opts)=>{c.calls.push(opts);return{action:opts.action,results:sessionsOf(opts).map(session=>({session,ok:!!opts.force,error:'open-pr: tracked'})),succeeded:0,failed:0}}},{action:'close'})
 await c.controller.run();assert.equal(need(c.calls[0]).force,undefined)
 await c.controller.run(true);assert.equal(need(c.calls[1]).force,true)
})
test('destroy suppresses late responses and stops future chunks without cancelling submitted work',async()=>{
 const response=deferred<BulkSessionResult>();const c=setup(600,{bulkSessionAction:(_room,opts)=>{c.calls.push(opts);return response.promise}})
 const pending=c.controller.run();const before=c.value;c.controller.destroy();response.resolve({action:'stop',results:sessionsOf(need(c.calls[0])).map(session=>({session,ok:true})),succeeded:0,failed:0});await pending
 assert.equal(c.value,before);assert.equal(c.calls.length,1)
})
test('stale transfer polls cannot overwrite a later retry and reject another operation ID',async()=>{
 const old=deferred<TransferState>();const c=setup(1,{bulkSessionAction:async()=>({action:'transfer',results:[{session:'s0',ok:true,transfer:transfer('old','waiting')}],succeeded:1,failed:0}),sessionTransfer:()=>old.promise},{action:'transfer'})
 await c.controller.load();c.controller.configure({profile:'model'});await c.controller.run();const pending=c.controller.refreshTransfers()
 // A different operation is a refused progress update, never a replacement.
 old.resolve(transfer('different','completed'));await pending
 assert.equal(need(c.value.results[0]).transfer?.id,'old');assert.match(need(c.value.results[0]).progressError ?? '',/Couldn’t check/)
})

test('accepted transfers progress to a confirmed ready state through saved status polling',async()=>{
 const c=setup(1,{bulkSessionAction:async()=>({action:'transfer',results:[{session:'s0',ok:true,transfer:transfer('t','waiting')}],succeeded:1,failed:0}),sessionTransfer:async()=>transfer('t','completed')},{action:'transfer'})
 await c.controller.load();c.controller.configure({profile:'model'});await c.controller.run();await c.controller.refreshTransfers()
 const first=need(c.value.results[0])
 assert.equal(first.transfer?.phase,'completed');assert.equal(bulkResultText(first,'transfer'),'New model ready')
})

// Move under…: each open session is a target, unless the move would make a
// cycle or pass subagent.maxDepth. A refused target says why.
test('adopt targets refuse cycles and depth past the limit, with a reason', () => {
  const m = (id: string, depth: number, parent = '', extra: Parameters<typeof member>[1] = {}) => member(id, { name: id, depth, parent, ...extra })
  // Depths as the Members API sends them: a top-level session is at display
  // depth 0, while its stored session depth is 1 (roomsvc.membersOf).
  const members = [
    m('owner', 0, '', { owner: true }), m('root', 0, '', { role: 'root', service: true }),
    m('lead', 0), m('a', 0), m('b', 1, 'a'), m('c', 2, 'b'), m('deep', 1, 'lead'), m('done', 0, '', { closed: true }),
  ]
  const why = (ids: string[], max = 3) => Object.fromEntries(adoptTargets(members, ids, max).map((t) => [t.member.id, t.reason]))
  // a's subtree is a, b, c: under lead it would reach depth 4.
  assert.deepEqual(why(['a']), { root: 'c would be at depth 4 of 3', lead: 'c would be at depth 4 of 3', b: 'Inside the selection', c: 'Inside the selection', deep: 'c would be at depth 5 of 3' })
  assert.deepEqual(why(['b']), { root: '', lead: '', a: '', c: 'Inside the selection', deep: 'c would be at depth 4 of 3' })
  assert.equal(why(['a'], 0).lead, '', 'no limit when maxDepth is 0')
})

test('adopt needs one chosen parent and sends it with the batch', async () => {
  const c = setup(1, { settings: async () => [{ key: 'subagent.maxDepth', value: 4 }] }, { action: 'adopt' })
  await c.controller.load()
  assert.equal(c.value.maxDepth, 4)
  await c.controller.run()
  assert.equal(c.calls.length, 0)
  assert.match(c.value.error, /parent/)
  c.controller.configure({ parent: 'lead' })
  await c.controller.run()
  assert.equal(need(c.calls[0]).parent, 'lead')
  assert.equal(bulkResultText({ ok: true }, 'adopt'), 'Moved')
})
