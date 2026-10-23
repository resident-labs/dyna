import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { selectableSession } from '../sessions/consolidation.ts'
import { createTestServer } from '../../../test/ssr-server.ts'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
const source=readFileSync(new URL('./Members.svelte',import.meta.url),'utf8')
const nodes=parse(source,{modern:true}).instance?.content.body ?? []
const handlers=nodes.filter(n=>n.type==='FunctionDeclaration'&&['rowClick','selectionKeydown'].includes(n.id?.name ?? '')).map(n=>{const {start,end}=sourceRange(n);return source.slice(start,end)}).join('\n')

interface Member { id?: string; closed?: boolean }
type Call = (string | boolean | undefined)[]

interface RowEvent {
  currentTarget: object
  target: { closest: () => object | null }
  preventDefault(): void
  ctrlKey?: boolean
  metaKey?: boolean
}

interface KeyEvent {
  key: string
  preventDefault?(): void
  stopPropagation?(): void
  defaultPrevented?: boolean
}

interface MembersState {
  selecting: boolean
  pickedSet: Set<string>
  selectableSession: typeof selectableSession
  onselect: (member: Member) => number
  onpick: (member: Member, checked: boolean, begin: boolean) => number
  onselection: () => number
}

interface MembersApp extends MembersState {
  rowClick: (event: RowEvent, member: Member) => void
  selectionKeydown: (event: KeyEvent) => void
}

function memberList(selecting=false,picked=[] as string[]) {
 const calls: Call[]=[];const c: MembersState={selecting,pickedSet:new Set(picked),selectableSession,onselect:m=>calls.push(['open',m.id]),onpick:(m,checked,begin)=>calls.push(['pick',m.id,checked,begin]),onselection:()=>calls.push(['cancel'])}
 vm.createContext(c);vm.runInContext(stripTypes(handlers),c)
 const fns=vm.runInContext('({ rowClick, selectionKeydown })',c) as Pick<MembersApp,'rowClick'|'selectionKeydown'>
 const row={};const event: RowEvent={currentTarget:row,target:{closest:()=>row},preventDefault(){calls.push(['prevent'])},ctrlKey:false,metaKey:false}
 return {c:Object.assign(c,fns),calls,event}
}
test('ordinary rows navigate; Select mode and Ctrl/Cmd-click toggle without navigating',()=>{
 const normal=memberList();normal.c.rowClick(normal.event,{id:'one'});assert.deepEqual(normal.calls,[['open','one']])
 for(const modifier of ['selecting','ctrlKey','metaKey'] as const){
  const x=memberList(modifier==='selecting',['one']);if(modifier!=='selecting')x.event[modifier]=true
  x.c.rowClick(x.event,{id:'one'});assert.deepEqual(x.calls,[['prevent'],['pick','one',false,modifier!=='selecting']])
 }
})
test('nested controls and closed rows cannot trigger selection or navigation',()=>{
 const x=memberList(true);x.event.target.closest=()=>({});x.c.rowClick(x.event,{id:'one'});assert.deepEqual(x.calls,[])
 const closed=memberList(true);closed.c.rowClick(closed.event,{id:'one',closed:true});assert.deepEqual(closed.calls,[['prevent']])
})
test('Escape exits selection before the phone panel shortcut, while handled menu keys stay separate',()=>{
 const x=memberList(true);let stopped=false;x.c.selectionKeydown({key:'Escape',preventDefault(){},stopPropagation(){stopped=true}})
 assert.deepEqual(x.calls,[['cancel']]);assert.equal(stopped,true)
 x.c.selectionKeydown({key:'Escape',defaultPrevented:true});assert.equal(x.calls.length,1)
})

