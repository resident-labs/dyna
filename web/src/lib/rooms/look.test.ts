import test from 'node:test'
import assert from 'node:assert/strict'
import { kindOf, setting, readLook, visibleTo, oneLine, highlighted, withChoice } from './look.ts'

const done = { completion: true, kind: 'completed', speaker: 'ui-guy-right', to: ['room'] }
const rootDone = { completion: true, kind: 'completed', speaker: 'root', to: ['room'] }
const failed = { completion: true, kind: 'failed', speaker: 'ui-guy-right' }
const pr = { kind: 'pr' }
const goal = { kind: 'goal' }
const chat = { kind: 'post' }
import type { Look } from './look.ts'

const empty = (): Look => ({ all: {} })

test('posts map to their choice; failures, chat and root results always show', () => {
  assert.deepEqual([done, pr, goal, failed, chat, rootDone].map(kindOf), ['finished', 'prs', 'goals', '', '', ''])
})

test('defaults: root-only highlight, worker results on one line, updates shown', () => {
  const look = empty()
  assert.equal(setting(look, 'highlight'), 'root')
  assert.equal(setting(look, 'finished'), 'line')
  assert.equal([done, pr, goal, failed, chat].every(visibleTo(look)), true)
  assert.equal(oneLine(look, done), true)
  assert.equal(oneLine(look, rootDone), false)
  assert.equal(oneLine(look, failed), false)
})

test('Root only highlights root’s posts to the owner; Everyone to you highlights all; Off none', () => {
  let look = empty()
  assert.equal(highlighted(look, rootDone, 'room'), true)
  assert.equal(highlighted(look, done, 'room'), false)
  assert.equal(highlighted(look, { ...rootDone, owner: true }, 'room'), false)
  look = withChoice(look, 'highlight', 'all')
  assert.equal(highlighted(look, done, 'room'), true)
  assert.equal(highlighted(look, { ...done, to: ['room:root'] }, 'room'), false)
  look = withChoice(look, 'highlight', 'off')
  assert.equal(highlighted(look, rootDone, 'room'), false)
})

test('a choice applies to every channel, and clearing it restores the default', () => {
  let look = withChoice(empty(), 'finished', 'hidden')
  assert.equal(setting(look, 'finished'), 'hidden')
  assert.equal(visibleTo(look)(done), false)
  look = withChoice(look, 'finished', 'full')
  assert.equal(setting(look, 'finished'), 'full')
  look = withChoice(look, 'finished', null)
  assert.equal(setting(look, 'finished'), 'line')
})

test('hidden kinds drop out; root results, failures and chat stay', () => {
  const look = withChoice(withChoice(empty(), 'prs', false), 'finished', 'hidden')
  assert.deepEqual([done, pr, goal, failed, chat, rootDone].filter(visibleTo(look)), [goal, failed, chat, rootDone])
})

test('the reader returns a saved on/off finished choice as saved, with no conversion', () => {
  assert.equal(setting({ all: { finished: false } }, 'finished'), false)
})

test('per-channel overrides a browser saved before the look became global no longer apply', () => {
  // The stored shape had a `channels` map keyed by room and view.
  const saved = readLook('{"all":{"finished":"hidden"},"channels":{"room_a:general":{"finished":"full"}}}')
  assert.deepEqual(saved, { all: { finished: 'hidden' } })
  assert.equal(setting(saved, 'finished'), 'hidden')
  assert.deepEqual(readLook('not json'), readLook(null), 'a broken value falls back to the defaults')
})

test('a post a jump revealed shows although its kind is hidden', () => {
  const look = withChoice(empty(), 'finished', 'hidden')
  const hidden = { id: 'msg_1', completion: true, kind: 'completed' }
  assert.equal(visibleTo(look)(hidden), false)
  assert.equal(visibleTo(look, { msg_1: true })(hidden), true)
})
