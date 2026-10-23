import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { addComment, commentLine, sendLabel, sendPending, type DiffComment } from './diffComments.ts'

const c = (n: number): DiffComment => ({ file: 'a.go', line: n, quote: '+x', text: `note ${n}` })

test('comments stay pending and make no API call before Send', async () => {
  const calls: unknown[][] = []
  const api = { sendDiffComments: async (...a: unknown[]) => calls.push(a) }
  let pending: DiffComment[] = []
  pending = addComment(addComment(pending, c(1)), c(2))
  assert.equal(pending.length, 2)
  assert.equal(sendLabel(pending.length, 'ana'), 'Send 2 comments to ana')
  assert.equal(sendLabel(1, ''), 'Send 1 comment')
  assert.equal(calls.length, 0)
  await sendPending((comments) => api.sendDiffComments('room', 'chg', comments), () => pending, (p) => (pending = p))
  assert.equal(calls.length, 1)
  assert.deepEqual(calls[0], ['room', 'chg', [c(1), c(2)]])
  assert.deepEqual(pending, [])
})

test('Discard sends nothing and an empty batch makes no call', async () => {
  const calls: number[] = []
  const api = { sendDiffComments: async (_room: string, _change: string, _comments: DiffComment[]) => calls.push(1) }
  await sendPending((comments) => api.sendDiffComments('room', 'chg', comments), () => [], () => {})
  assert.equal(calls.length, 0)
})

test('a removed line points at its old line number', () => {
  assert.equal(commentLine({ a: 4, b: 0, t: '-' }), 4)
  assert.equal(commentLine({ a: 4, b: 5, t: ' ' }), 5)
})

test('DiffView shows the + only with an oncomment prop', () => {
  const src = readFileSync(new URL('./DiffView.svelte', import.meta.url), 'utf8')
  assert.match(src, /\{#if oncomment[^}]*\}[\s\S]*class="addc"/)
})

test('PRFiles has the send bar with Send and Discard, and the API names the call', () => {
  const src = readFileSync(new URL('../prs/PRFiles.svelte', import.meta.url), 'utf8')
  assert.match(src, /<SendBar/)
  assert.match(readFileSync(new URL('../files/SendBar.svelte', import.meta.url), 'utf8'), /sendLabel\(pending\.length[\s\S]*Discard/)
  assert.match(readFileSync(new URL('../../api.ts', import.meta.url), 'utf8'), /sendDiffComments:.*app\.SendDiffComments/)
})

test('owner findings form the first group, "Your comments"', async () => {
  const { groupFindings } = await import('../prs/pr.ts')
  const g = groupFindings([{ id: 'f1', severity: 'nit' }, { id: 'o1', severity: 'owner' }])
  assert.deepEqual(g.map((x) => [x.id, x.label]), [['owner', 'Your comments'], ['nit', 'Nit']])
})

test('on touch screens the + shows only on the tapped line and not over the numbers', () => {
  const src = readFileSync(new URL('./DiffView.svelte', import.meta.url), 'utf8')
  const phone = src.slice(src.indexOf('@media (max-width: 760px)'))
  assert.match(phone, /\.addc \{ opacity: 0; pointer-events: none;/)
  assert.match(phone, /\.ln\.sel \.addc, \.addc:focus-visible \{ opacity: 1; pointer-events: auto; \}/)
  assert.doesNotMatch(phone, /\.addc \{[^}]*display: none/) // stays in the tab order
  assert.match(phone, /\.addc \{[^}]*right: 6px/)
  assert.doesNotMatch(phone, /\.addc \{[^}]*opacity: 1/) // untapped lines stay transparent and ignore taps
  assert.match(src, /@media \(hover: hover\) \{ \.ln:hover \.addc/)
  assert.match(src, /key === picked \? 'sel '/)
})

test('comments added while Send runs stay pending', async () => {
  const { promise, resolve } = Promise.withResolvers<void>()
  const api = { sendDiffComments: (_room: string, _change: string, _comments: DiffComment[]) => promise }
  let pending: DiffComment[] = [c(1)]
  const done = sendPending((comments) => api.sendDiffComments('room', 'chg', comments), () => pending, (p) => (pending = p))
  pending = addComment(pending, c(2))
  resolve()
  await done
  assert.deepEqual(pending, [c(2)])
})
