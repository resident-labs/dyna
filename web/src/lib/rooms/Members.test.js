import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { selectableSession } from '../sessions/consolidation.ts'
import { createTestServer } from '../../../test/ssr-server.ts'
const source=readFileSync(new URL('./Members.svelte',import.meta.url),'utf8')
const handlers=parse(source).instance.content.body.filter(n=>n.type==='FunctionDeclaration'&&['rowClick','selectionKeydown'].includes(n.id.name)).map(n=>source.slice(n.start,n.end)).join('\n')
function memberList(selecting=false,picked=[]) {
 const calls=[];const c={selecting,pickedSet:new Set(picked),selectableSession,onselect:m=>calls.push(['open',m.id]),onpick:(m,checked,begin)=>calls.push(['pick',m.id,checked,begin]),onselection:()=>calls.push(['cancel'])}
 vm.createContext(c);vm.runInContext(handlers,c)
 const row={};const event={currentTarget:row,target:{closest:()=>row},preventDefault(){calls.push(['prevent'])}}
 return {c,calls,event}
}
test('ordinary rows navigate; Select mode and Ctrl/Cmd-click toggle without navigating',()=>{
 const normal=memberList();normal.c.rowClick(normal.event,{id:'one'});assert.deepEqual(normal.calls,[['open','one']])
 for(const modifier of ['selecting','ctrlKey','metaKey']){
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

