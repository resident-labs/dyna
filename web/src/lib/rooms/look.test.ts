import test from 'node:test'
import assert from 'node:assert/strict'
import { kindOf, setting, differs, visibleTo, oneLine, highlighted, withChoice, scopeOf } from './look.ts'

const done = { completion: true, kind: 'completed', speaker: 'ui-guy-right', to: ['room'] }
const rootDone = { completion: true, kind: 'completed', speaker: 'root', to: ['room'] }
const failed = { completion: true, kind: 'failed', speaker: 'ui-guy-right' }
const pr = { kind: 'pr' }
const goal = { kind: 'goal' }
const chat = { kind: 'post' }
import type { Look } from './look.ts'

const empty = (): Look => ({ all: {}, channels: {} })

test('posts map to their choice; failures, chat and root results always show', () => {
  assert.deepEqual([done, pr, goal, failed, chat, rootDone].map(kindOf), ['finished', 'prs', 'goals', '', '', ''])
})

test('defaults: root-only highlight, worker results on one line, updates shown', () => {
  const look = empty()
  assert.equal(setting(look, 'general', 'highlight'), 'root')
  assert.equal(setting(look, 'general', 'finished'), 'line')
  assert.equal([done, pr, goal, failed, chat].every(visibleTo(look, 'general')), true)
  assert.equal(oneLine(look, 'general', done), true)
  assert.equal(oneLine(look, 'general', rootDone), false)
  assert.equal(oneLine(look, 'general', failed), false)
})

test('Root only highlights root’s posts to the owner; Everyone to you highlights all; Off none', () => {
  let look = empty()
  assert.equal(highlighted(look, 's', rootDone, 'room'), true)
  assert.equal(highlighted(look, 's', done, 'room'), false)
  assert.equal(highlighted(look, 's', { ...rootDone, owner: true }, 'room'), false)
  look = withChoice(look, null, 'highlight', 'all')
  assert.equal(highlighted(look, 's', done, 'room'), true)
  assert.equal(highlighted(look, 's', { ...done, to: ['room:root'] }, 'room'), false)
  look = withChoice(look, 's', 'highlight', 'off')
  assert.equal(highlighted(look, 's', rootDone, 'room'), false)
})

test('a channel choice beats the default, and clearing it restores the default', () => {
  let look = withChoice(empty(), null, 'finished', 'hidden')
  assert.equal(setting(look, 'general', 'finished'), 'hidden')
  look = withChoice(look, 'general', 'finished', 'full')
  assert.deepEqual(differs(look, 'general'), ['finished'])
  assert.equal(setting(look, 'channel:a', 'finished'), 'hidden')
  look = withChoice(look, 'general', 'finished', null)
  assert.deepEqual(look.channels, {})
  assert.deepEqual(differs(look, 'general'), [])
})

test('hidden kinds drop out; root results, failures and chat stay', () => {
  const look = withChoice(withChoice(empty(), 'general', 'prs', false), 'general', 'finished', 'hidden')
  assert.deepEqual([done, pr, goal, failed, chat, rootDone].filter(visibleTo(look, 'general')), [goal, failed, chat, rootDone])
})

test('the reader returns a saved on/off finished choice as saved, with no conversion', () => {
  assert.equal(setting({ all: { finished: false }, channels: {} }, 's', 'finished'), false)
  assert.equal(setting({ all: {}, channels: { s: { finished: true } } }, 's', 'finished'), true)
})

test('one room\'s general channel does not change another room\'s', () => {
  const a = scopeOf('room_a', 'general')
  const b = scopeOf('room_b', 'general')
  const look = withChoice(empty(), a, 'finished', 'hidden')
  assert.equal(setting(look, a, 'finished'), 'hidden')
  assert.equal(setting(look, b, 'finished'), 'line')
})

test('a post a jump revealed shows although its kind is hidden', () => {
  const look = withChoice(empty(), null, 'finished', 'hidden')
  const hidden = { id: 'msg_1', completion: true, kind: 'completed' }
  assert.equal(visibleTo(look, 'room:general')(hidden), false)
  assert.equal(visibleTo(look, 'room:general', { msg_1: true })(hidden), true)
})
