import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import type { Post } from '../api/room.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const fn = (parse(source, { modern: true }).instance?.content.body ?? []).find((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'startReply')
assert.ok(fn, 'App has a startReply function')
const { start, end } = sourceRange(fn)
const startReplySource = stripTypes(source.slice(start, end))

// startReply reads only the sender address and the two owner flags.
type ReplyPost = Partial<Post> & { from: string }

interface ReplyContext {
  room: { name: string }
  draft: { value: string }
  reply: ReplyPost | null
  composer: { focus(): void }
}

function replyTo(post: ReplyPost): string {
  const context: ReplyContext = { room: { name: 'alpha' }, draft: { value: '@old Keep this draft' }, reply: null, composer: { focus() {} } }
  vm.createContext(context)
  vm.runInContext(startReplySource, context)
  const startReply = vm.runInContext('startReply', context) as (post: ReplyPost) => void
  startReply(post)
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
