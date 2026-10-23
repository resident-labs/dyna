import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const fn = parse(source).instance.content.body.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'startReply')
function replyTo(post) {
  const context = { room: { name: 'alpha' }, draft: { value: '@old Keep this draft' }, reply: null, composer: { focus() {} } }
  vm.createContext(context)
  vm.runInContext(stripTypes(source.slice(fn.start, fn.end)), context)
  context.startReply(post)
  assert.equal(context.reply, post)
  return context.draft.value
}
test('replies target a room sender and preserve the draft body', () => {
  assert.equal(replyTo({ from: 'alpha:worker' }), '@worker Keep this draft')
  assert.equal(replyTo({ from: 'alpha', owner: true }), '@root Keep this draft')
  assert.equal(replyTo({ from: 'alpha', owner: true, human: true }), '@old Keep this draft')
})
test('system posts reply to root instead of slicing an unrelated sender address', () => {
  for (const from of ['goal-keeper', 'shell:sh_1', 'run:wf_1', 'github:review', 'future-system']) {
    assert.equal(replyTo({ from }), '@root Keep this draft', from)
  }
})
