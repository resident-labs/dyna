import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { TUCKED, COMPOSER, CHAT, chatClick, chatKey, chatDriving, lastLine } from './chatState.js'
import { dmView, type ThreadPost } from '../sessions/dm.ts'

type KeyEvent = { key: string; ctrl: boolean; meta: boolean; alt: boolean; editable: boolean; inside: boolean }
type Item =
  | { kind: 'prompt'; from: string; text: string; ts: string }
  | { kind: 'tool'; tool: string; target: string; ts: string }
  | { kind: 'reply'; text: string; ts: string }
type Session = {
  member: { name: string }
  address: string
  turns: { n: number; started: string; status?: string; items: Item[] }[]
  posts: ThreadPost[]
}

// need returns the value, or throws when a test expects it to exist.
function need<T>(value: T | null | undefined, what: string): T {
  if (value === null || value === undefined) throw new Error(`missing ${what}`)
  return value
}

const key = (k: string, o: Partial<KeyEvent> = {}): KeyEvent => ({ key: k, ctrl: false, meta: false, alt: false, editable: false, inside: false, ...o })

test('clicks move between the three shapes', () => {
  assert.equal(chatClick(TUCKED, 'pill'), CHAT)
  assert.equal(chatClick(COMPOSER, 'lastline'), CHAT)
  assert.equal(chatClick(CHAT, 'header'), TUCKED)
  assert.equal(chatClick(COMPOSER, 'stream'), TUCKED)
  assert.equal(chatClick(CHAT, 'stream'), TUCKED)
  assert.equal(chatClick(TUCKED, 'stream'), TUCKED)
})

test('/ opens the composer, Ctrl+/ opens the chat', () => {
  assert.deepEqual(chatKey(TUCKED, key('/'), false), { state: COMPOSER, handled: true })
  assert.deepEqual(chatKey(TUCKED, key('/', { ctrl: true }), false), { state: CHAT, handled: true })
  assert.deepEqual(chatKey(COMPOSER, key('/', { ctrl: true }), false), { state: CHAT, handled: true })
  assert.deepEqual(chatKey(CHAT, key('/', { meta: true }), false), { state: CHAT, handled: true })
})

test('/ typed in a text field is just a character', () => {
  assert.equal(chatKey(TUCKED, key('/', { editable: true }), false).handled, false)
  assert.equal(chatKey(COMPOSER, key('/', { editable: true, inside: true }), false).handled, false)
  assert.equal(chatKey(CHAT, key('/'), false).handled, false, 'already open')
})

test('Esc tucks the composer and the chat', () => {
  assert.deepEqual(chatKey(COMPOSER, key('Escape', { inside: true }), false), { state: TUCKED, handled: true })
  assert.deepEqual(chatKey(CHAT, key('Escape'), false), { state: TUCKED, handled: true })
  assert.equal(chatKey(TUCKED, key('Escape'), false).handled, false, 'nothing to tuck')
})

test('driving: keys go to the computer', () => {
  assert.equal(chatKey(TUCKED, key('/'), true).handled, false)
  assert.equal(chatKey(TUCKED, key('Escape'), true).handled, false)
  assert.equal(chatKey(CHAT, key('Escape'), true).handled, false, 'Esc reaches the computer')
  assert.equal(chatKey(CHAT, key('a'), true).handled, false)
})

test('driving: Ctrl+/ still reaches the chat, and Esc works inside it', () => {
  assert.deepEqual(chatKey(TUCKED, key('/', { ctrl: true }), true), { state: CHAT, handled: true })
  assert.deepEqual(chatKey(CHAT, key('Escape', { inside: true }), true), { state: TUCKED, handled: true })
})

test('taking control tucks the chat; sending does not return control', () => {
  assert.equal(chatDriving(CHAT, true), TUCKED)
  assert.equal(chatDriving(COMPOSER, true), TUCKED)
  assert.equal(chatDriving(CHAT, false), CHAT, 'leaving control keeps the shape')
})

test('the glass gets more opaque as the chat grows', () => {
  const css = readFileSync(new URL('../ui/tokens.css', import.meta.url), 'utf8')
  const level = (n: string): number => Number(need(css.match(new RegExp(`--glass-${n}:\\s*([\\d.]+)`))?.[1], `--glass-${n}`))
  assert.ok(level('pill') < level('composer') && level('composer') < level('panel'))
  assert.deepEqual([level('pill'), level('composer'), level('panel')], [0.86, 0.93, 0.95])
})

const room = { name: 'dyna-agent' }
const prompt: Item = { kind: 'prompt', from: 'dyna-agent', text: 'Test the coupon flow', ts: '2026-10-17T18:00:01Z' }
const session: Session = {
  member: { name: 'lets-work' }, address: 'dyna-agent:lets-work',
  turns: [{ n: 1, started: '2026-10-17T18:00:00Z', status: 'running', items: [
    prompt,
    { kind: 'tool', tool: 'click', target: '"Upgrade to Team"', ts: '2026-10-17T18:00:02Z' },
    { kind: 'tool', tool: 'type', target: '"SPRING25"', ts: '2026-10-17T18:00:03Z' },
  ] }],
  posts: [],
}

test('last line is the newest agent action or reply', () => {
  const dm = dmView(session, room)
  const l = lastLine(dm.rows, dm.address)
  assert.equal(need(l, 'last line').who, 'act')
  assert.equal(need(l, 'last line').text, '', 'an action carries no tool argument')
  const more = structuredClone(session)
  need(more.turns[0], 'first turn').items.push({ kind: 'reply', text: 'Got **it**, switching to SAVE10.', ts: '2026-10-17T18:00:04Z' })
  const dm2 = dmView(more, room)
  assert.deepEqual(lastLine(dm2.rows, dm2.address), { key: 'item:1:3', who: 'say', text: 'Got it, switching to SAVE10.' })
  assert.notEqual(need(lastLine(dm2.rows, dm2.address), 'last line').key, need(l, 'last line').key, 'a new line changes the key, which sets the unread dot')
  need(more.turns[0], 'first turn').items.push({ kind: 'tool', tool: 'click', target: 'Apply', ts: '2026-10-17T18:00:05Z' })
  const dm3 = dmView(more, room)
  assert.equal(need(lastLine(dm3.rows, dm3.address), 'new action').who, 'act', 'a later step in the earlier timeline row still marks new activity')
  assert.equal(lastLine([], 'x'), null)
})

test('the owner message is not the last line', () => {
  const only = { member: session.member, turns: [{ n: 1, started: '2026-10-17T18:00:00Z', items: [prompt] }], posts: [] }
  const dm = dmView(only, room)
  assert.equal(lastLine(dm.rows, dm.address), null)
})
