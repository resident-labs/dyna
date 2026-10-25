import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { short } from '../ui/format.ts'
import type { Post, ReplyTarget } from '../api/room.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const fn = (parse(source, { modern: true }).instance?.content.body ?? []).find((n) => n.type === 'FunctionDeclaration' && n.id?.name === 'startReply')
assert.ok(fn, 'App has a startReply function')
const { start, end } = sourceRange(fn)
const startReplySource = stripTypes(source.slice(start, end))

// startReply reads the reply target the server stores for the post, and the
// member list that gives a renamed session its current address.
type ReplyPost = Partial<Post> & { from: string; reply?: ReplyTarget }

type ReplyMember = { id: string; address: string }

interface ReplyContext {
  room: { name: string }
  draft: { value: string }
  reply: ReplyPost | null
  composer: { focus(): void }
  members: ReplyMember[]
  short: typeof short
}

function replyTo(post: ReplyPost, members: ReplyMember[] = []): string {
  const context: ReplyContext = { room: { name: 'alpha' }, draft: { value: '@old Keep this draft' }, reply: null, composer: { focus() {} }, members, short }
  vm.createContext(context)
  vm.runInContext(startReplySource, context)
  const startReply = vm.runInContext('startReply', context) as (post: ReplyPost) => void
  startReply(post)
  assert.equal(context.reply, post)
  return context.draft.value
}
test('replies target a room sender and preserve the draft body', () => {
  assert.equal(replyTo({ from: 'alpha:worker', reply: { kind: 'session', id: 'ses_1', address: 'alpha:worker' } }), '@worker Keep this draft')
  assert.equal(replyTo({ from: 'alpha', owner: true, reply: { kind: 'room', address: 'alpha' } }), '@root Keep this draft')
  assert.equal(replyTo({ from: 'alpha', owner: true, human: true }), '@old Keep this draft')
})
test('a reply to a session in another room names that session, not root', () => {
  assert.equal(replyTo({ from: 'beta:other', reply: { kind: 'session', id: 'ses_2', address: 'beta:other' } }), '@beta:other Keep this draft')
})
test('a renamed session takes the reply at its current address', () => {
  const members = [{ id: 'ses_1', address: 'alpha:builder' }]
  assert.equal(replyTo({ from: 'alpha:worker', reply: { kind: 'session', id: 'ses_1', address: 'alpha:worker' } }, members), '@builder Keep this draft')
})
test('system posts reply to root instead of slicing an unrelated sender address', () => {
  for (const from of ['goal-keeper', 'shell:sh_1', 'run:wf_1', 'github:review', 'future-system']) {
    assert.equal(replyTo({ from, reply: { kind: 'room', address: 'alpha' } }), '@root Keep this draft', from)
  }
})
