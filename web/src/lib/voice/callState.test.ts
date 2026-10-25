import { test } from 'node:test'
import assert from 'node:assert/strict'
import { capturing, initial, offerCall, reduce, spaceTalk, statusLine, type CallEvent, type TalkKey, type TalkTarget } from './callState.ts'

const run = (...events: CallEvent[]) => events.reduce(reduce, initial)
const live = (...more: CallEvent[]) => run({ type: 'start', roomId: 'r1', room: 'voice-test' }, { type: 'started', startedAt: 1000 }, { type: 'voiceReady' }, ...more)

test('a call goes idle → starting → connecting → live → ending → idle', () => {
  let s = run({ type: 'start', roomId: 'r1', room: 'voice-test' })
  assert.equal(s.phase, 'starting')
  assert.equal(statusLine(s), 'Starting Voice…')
  s = reduce(s, { type: 'started', startedAt: 1000 })
  assert.equal(statusLine(s), 'Connecting…')
  assert.equal(s.startedAt, 1000)
  s = reduce(s, { type: 'voiceReady' })
  assert.equal(s.phase, 'live')
  assert.equal(statusLine(s), 'Listening')
  s = reduce(s, { type: 'activity', value: 'thinking' })
  assert.equal(statusLine(s), 'Thinking')
  s = reduce(reduce(s, { type: 'hangup' }), { type: 'closed', reason: '' })
  assert.equal(s.phase, 'idle')
})

test('a dropped call is an error until dismissed; an occupied seat says where', () => {
  let s = live({ type: 'closed', reason: '' })
  assert.equal(s.phase, 'error')
  assert.equal(statusLine(s), 'The call dropped.')
  assert.equal(reduce(s, { type: 'dismiss' }).phase, 'idle')
  s = live({ type: 'closed', occupied: true })
  assert.match(statusLine(s), /open in another window/)
  s = live({ type: 'serverError', code: 'client_occupied', message: 'x' })
  assert.equal(s.phase, 'error')
})

test('a busy start keeps the room that holds the call', () => {
  const s = run({ type: 'start', roomId: 'r2', room: 'b' }, { type: 'startFailed', error: 'a voice call is already live in room a', busyRoom: 'a' })
  assert.equal(s.phase, 'error')
  assert.equal(s.busyRoom, 'a')
  assert.equal(reduce(s, { type: 'start', roomId: 'r2', room: 'b' }).busyRoom, '')
})

test('a lost voice link reconnects without ending the call', () => {
  const s = live({ type: 'voiceLost', message: '' })
  assert.equal(s.phase, 'connecting')
  assert.equal(statusLine(s), 'Reconnecting…')
  assert.equal(reduce(s, { type: 'voiceReady' }).startedAt, 1000)
})

test('mute, deafen and push to talk gate the microphone', () => {
  assert.ok(capturing(live()))
  assert.ok(!capturing(run({ type: 'start', roomId: 'r1', room: 'x' })))
  let s = live({ type: 'toggleMute' })
  assert.ok(!capturing(s))
  assert.equal(statusLine(s), 'Muted')
  s = reduce(s, { type: 'toggleMute' })
  assert.ok(capturing(s))
  s = reduce(s, { type: 'toggleDeafen' })
  assert.ok(s.muted && s.deafened && !capturing(s))
  assert.equal(statusLine(s), 'Muted')
  s = reduce(s, { type: 'toggleDeafen' })
  assert.ok(!s.muted && !s.deafened)
  s = reduce(s, { type: 'pushToTalk', on: true })
  assert.ok(!capturing(s))
  // The status line shows only the state; the mic button says how to talk.
  assert.equal(statusLine(s), 'Muted')
  s = reduce(s, { type: 'talk', on: true })
  assert.ok(capturing(s))
  assert.equal(statusLine(s), 'Listening')
  assert.equal(reduce(live(), { type: 'talk', on: true }).talking, false)
})

test('controls carry over to the next call', () => {
  const s = live({ type: 'pushToTalk', on: true }, { type: 'hangup' }, { type: 'ended' }, { type: 'start', roomId: 'r1', room: 'x' })
  assert.equal(s.pushToTalk, true)
  assert.equal(s.phase, 'starting')
})

test('the header offers a call only when voice is set up', () => {
  // Without a ChatGPT account, or before the status
  // arrives, the header shows nothing new.
  assert.equal(offerCall(initial, 'r1'), '')
  assert.equal(offerCall(run({ type: 'status', available: false }), 'r1'), '')
  // Setting voice up later turns the button on.
  assert.equal(offerCall(run({ type: 'status', available: false }, { type: 'status', available: true }), 'r1'), 'call')
  assert.equal(offerCall(live(), 'r1'), 'live')
  assert.equal(offerCall(live(), 'r2'), 'call', 'another room may ask to switch')
  assert.equal(offerCall(run({ type: 'start', roomId: 'r1', room: 'x' }), 'r1'), 'calling')
})

test('a call from another room asks first, and only during a call', () => {
  let s = live({ type: 'askSwitch', roomId: 'r2', room: 'b' })
  assert.deepEqual(s.switchTo, { roomId: 'r2', room: 'b' })
  assert.equal(s.phase, 'live')
  assert.equal(reduce(s, { type: 'cancelSwitch' }).switchTo, null)
  assert.equal(reduce(s, { type: 'hangup' }).switchTo, null)
  assert.equal(live({ type: 'askSwitch', roomId: 'r1', room: 'voice-test' }).switchTo, null, 'same room')
  assert.equal(run({ type: 'askSwitch', roomId: 'r2', room: 'b' }).switchTo, null, 'no call')
})

test('Space talks only with push to talk on, never starts from a text field, and always releases', () => {
  const ptt = live({ type: 'pushToTalk', on: true })
  const talking = reduce(ptt, { type: 'talk', on: true })
  const page = { closest: () => null }
  const field = (sel: string) => ({ closest: (q: string) => (q.split(', ').some((part) => part.startsWith(sel)) ? {} : null) })
  const key = (type: string, target: TalkTarget, more: { repeat?: boolean } = {}): TalkKey => ({ type, code: 'Space', target, ...more })
  assert.equal(spaceTalk(key('keydown', page), ptt), true)
  assert.equal(spaceTalk(key('keyup', page), talking), false)
  assert.equal(spaceTalk(key('keyup', page), ptt), null, 'nothing to release')
  assert.equal(spaceTalk(key('keydown', page, { repeat: true }), talking), null)
  for (const sel of ['input', 'textarea', 'select', '[contenteditable]']) {
    assert.equal(spaceTalk(key('keydown', field(sel)), ptt), null, sel)
    // Hold Space, move focus into a field, release: the release still counts.
    assert.equal(spaceTalk(key('keyup', field(sel)), talking), false, sel)
  }
  assert.equal(spaceTalk(key('keydown', { isContentEditable: true, closest: () => null }), ptt), null)
  // Leaving the window or hiding the page loses the keyup.
  assert.equal(spaceTalk({ type: 'blur', target: {} }, talking), false)
  assert.equal(spaceTalk({ type: 'visibilitychange', target: { hidden: true } }, talking), false)
  assert.equal(spaceTalk({ type: 'visibilitychange', target: { hidden: false } }, talking), null)
  assert.equal(spaceTalk(key('keydown', page), live()), null, 'push to talk off')
  assert.equal(spaceTalk(key('keydown', page), run({ type: 'pushToTalk', on: true })), null, 'no live call')
  assert.equal(spaceTalk({ type: 'keydown', code: 'KeyA', target: page }, ptt), null)
})

test('a mode change unmutes, except in a deafened call', () => {
  assert.equal(live({ type: 'toggleMute' }, { type: 'pushToTalk', on: true }).muted, false)
  assert.equal(live({ type: 'pushToTalk', on: true }, { type: 'toggleMute' }, { type: 'pushToTalk', on: false }).muted, false)
  assert.equal(live({ type: 'toggleDeafen' }, { type: 'pushToTalk', on: true }).muted, true)
})

test('during a call the status line is only Listening, Speaking, Thinking or Muted', () => {
  const lines = new Set<string>()
  const cases: CallEvent[][] = [[], [{ type: 'toggleMute' }], [{ type: 'toggleDeafen' }], [{ type: 'pushToTalk', on: true }], [{ type: 'pushToTalk', on: true }, { type: 'talk', on: true }], [{ type: 'activity', value: 'speaking' }], [{ type: 'activity', value: 'thinking' }], [{ type: 'activity', value: 'idle' }]]
  for (const events of cases) {
    lines.add(statusLine(live(...events)))
  }
  assert.deepEqual([...lines].sort(), ['Listening', 'Muted', 'Speaking', 'Thinking'])
})

test('choosing the current mode keeps an explicit mute', () => {
  const s = live({ type: 'toggleMute' }, { type: 'pushToTalk', on: false })
  assert.equal(s.muted, true)
  assert.ok(!capturing(s))
})

test('a page load during a live call offers to resume it, and never starts audio itself', () => {
  const found: CallEvent = { type: 'status', available: true, live: true, roomId: 'r1', room: 'voice-test', startedAt: 1000 }
  const s = reduce(initial, found)
  assert.equal(s.phase, 'resumable')
  assert.deepEqual([s.roomId, s.room, s.startedAt], ['r1', 'voice-test', 1000])
  assert.equal(statusLine(s), 'Call in progress')
  assert.ok(!capturing(s))
  // The call ended elsewhere, or after 60 s with no page.
  assert.equal(reduce(s, { type: 'status', available: true, live: false }).phase, 'idle')
  // A status never changes a call this page already runs.
  assert.equal(reduce(live(), found).phase, 'live')
  // Rejoin starts the call in its room.
  assert.equal(reduce(s, { type: 'start', roomId: 'r1', room: 'voice-test' }).phase, 'starting')
})

test("Voice's tool calls are tracked by ID during a call, and a new call starts with none", () => {
  let s = reduce(reduce(initial, { type: 'start', roomId: 'r1', room: 'work' }), { type: 'started', startedAt: 1 })
  s = reduce(s, { type: 'tool', id: 'c1', name: 'agents', args: '{}', state: 'running', ts: 5 })
  s = reduce(s, { type: 'tool', id: 'c1', name: 'agents', args: '', state: 'done', ts: 9 })
  assert.deepEqual(s.tools, [{ id: 'c1', name: 'agents', args: '{}', state: 'done', ts: 5 }])
  s = reduce(reduce(s, { type: 'hangup' }), { type: 'ended' })
  assert.equal(reduce(s, { type: 'tool', id: 'c2', name: 'agents', args: '{}', state: 'running', ts: 10 }), s)
  assert.deepEqual(reduce(s, { type: 'start', roomId: 'r1', room: 'work' }).tools, [])
})
