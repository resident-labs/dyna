import { test } from 'node:test'
import assert from 'node:assert/strict'
import { RATE, audioMsg, cancelMsg, fromWire, hangupMsg, muteMsg, playbackMsg } from './wire.js'

test('engine messages become app events', () => {
  assert.deepEqual(fromWire({ type: 'state', state: 'thinking' }), { type: 'engineState', value: 'thinking' })
  assert.deepEqual(fromWire({ type: 'resume_cursor', cursor: 'm7' }), { type: 'resumeCursor', cursor: 'm7' })
  assert.equal(fromWire({ type: 'resume_cursor', cursor: 123 }), null)
  assert.deepEqual(fromWire({ type: 'state', state: 'reconnecting' }), { type: 'voiceLost', message: '' })
  assert.equal(fromWire({ type: 'state', state: 'connecting' }), null)
  assert.deepEqual(fromWire({ type: 'audio', id: 'r1', audio: 'AAA=' }), { type: 'audio', audio: 'AAA=', rate: RATE, responseId: 'r1' })
  assert.deepEqual(fromWire({ type: 'audio_done', id: 'r1' }), { type: 'audioDone', responseId: 'r1' })
  assert.deepEqual(fromWire({ type: 'clear', id: 'r1' }), { type: 'bargeIn', responseId: 'r1' })
  assert.deepEqual(fromWire({ type: 'transcript', text: 'is the build', final: false }), { type: 'partial', text: 'is the build' })
  assert.deepEqual(fromWire({ type: 'transcript', text: 'is the build green', final: true }), { type: 'partial', text: '' })
  assert.deepEqual(fromWire({ type: 'unspoken', post: 'm7' }), { type: 'unspoken', post: 'm7' })
  assert.deepEqual(fromWire({ type: 'tool', id: 'c1', name: 'pr_status', text: '{"number":7}', state: 'running' }), { type: 'tool', id: 'c1', name: 'pr_status', args: '{"number":7}', state: 'running' })
  assert.deepEqual(fromWire({ type: 'tool', id: 'c1', name: 'pr_status', state: 'done' }), { type: 'tool', id: 'c1', name: 'pr_status', args: '', state: 'done' })
  assert.deepEqual(fromWire({ type: 'error', message: 'upstream lost' }), { type: 'serverError', code: '', message: 'upstream lost' })
  assert.equal(fromWire({ type: 'session.ready' }), null)
  assert.equal(fromWire(null), null)
})

test('page messages follow the schema', () => {
  assert.deepEqual(audioMsg('AAA='), { type: 'audio', audio: 'AAA=' })
  assert.deepEqual(muteMsg(true), { type: 'mute', muted: true })
  assert.deepEqual(cancelMsg(), { type: 'cancel' })
  assert.deepEqual(playbackMsg('r1', 'ended', 812.4), { type: 'playback', id: 'r1', state: 'ended', played_ms: 812 })
  assert.deepEqual(playbackMsg('r2', 'ended'), { type: 'playback', id: 'r2', state: 'ended', played_ms: 0 })
  assert.deepEqual(hangupMsg(), { type: 'hangup' })
})
