import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createCall } from './call.js'

function fakeSocket() {
  const ws = { readyState: 0, sent: [], closed: null }
  ws.send = (m) => ws.sent.push(m instanceof ArrayBuffer ? m : JSON.parse(m))
  ws.close = (code) => { ws.closed = code }
  ws.open = () => { ws.readyState = 1; ws.onopen() }
  ws.recv = (m) => ws.onmessage({ data: JSON.stringify(m) })
  ws.drop = (code, reason = '') => { ws.readyState = 3; ws.onclose({ code, reason }) }
  return ws
}

function setup({ startError, status = {}, micError } = {}) {
  const calls = []
  const audio = { played: [], done: [], stops: [], deafened: [], closed: 0, receipts: null, heard: 0, unducked: 0 }
  let frame
  const ws = fakeSocket()
  const deps = {
    socket: (room) => { calls.push(['socket', room]); return ws },
    api: {
      voiceStart: async (room) => {
        calls.push(['start', room])
        if (startError) throw startError
        return { live: true, roomId: room, room: 'voice-test', startedAt: 42 }
      },
      voiceStatus: async () => status,
      voiceStop: async () => { calls.push(['stop']) },
    },
    openAudio: async (onFrame, receipts) => {
      if (micError) throw micError
      frame = onFrame
      audio.receipts = receipts
      return {
        play: (b64, rate, id) => audio.played.push([b64, rate, id]),
        done: (id) => audio.done.push(id),
        stopPlayback: (reason) => audio.stops.push(reason),
        setDeafened: (on) => audio.deafened.push(on),
        close: () => { audio.closed++ },
        hear: () => { audio.heard++ },
        unduck: () => { audio.unducked++ },
      }
    },
  }
  const call = createCall(deps)
  const mic = () => frame(new Float32Array(480).fill(0.25), 48000)
  return { call, ws, calls, audio, mic, deps }
}

async function liveCall(o) {
  const t = setup(o)
  await t.call.start('room_1', 'voice-test')
  t.ws.open()
  t.ws.recv({ type: 'state', state: 'listening' })
  return t
}

test("start asks the engine, opens the room's call socket and says whether the mic is open", async () => {
  const { call, ws, calls } = await liveCall()
  assert.deepEqual(calls, [['start', 'room_1'], ['socket', 'voice-test']])
  assert.deepEqual(ws.sent[0], { type: 'mute', muted: false })
  assert.equal(call.state.phase, 'live')
  assert.equal(call.state.startedAt, 42)
  ws.recv({ type: 'state', state: 'thinking' })
  assert.equal(call.state.activity, 'thinking')
})

test('microphone frames go out only while the mic is open, at 24 kHz', async () => {
  const { call, ws, mic } = await liveCall()
  const frames = () => ws.sent.filter((m) => m instanceof ArrayBuffer)
  mic()
  assert.equal(frames().length, 1)
  // 480 samples at 48 kHz are 240 at 24 kHz: 480 bytes of PCM16.
  assert.equal(frames()[0].byteLength, 480)
  call.toggleMute()
  mic()
  call.toggleMute()
  call.setPushToTalk(true)
  mic()
  assert.equal(frames().length, 1)
  call.talk(true)
  mic()
  assert.equal(frames().length, 2)
})

test("Voice's audio plays, the engine's clear stops it at once, and deafen silences it", async () => {
  const { call, ws, audio } = await liveCall()
  ws.recv({ type: 'audio', id: 'r1', audio: 'AAAA' })
  ws.recv({ type: 'audio_done', id: 'r1' })
  assert.deepEqual(audio.played, [['AAAA', 24000, 'r1']])
  assert.deepEqual(audio.done, ['r1'])
  ws.recv({ type: 'clear', id: 'r1' })
  call.interrupt()
  assert.deepEqual(audio.stops, ['clear', 'cancel'])
  assert.deepEqual(ws.sent.at(-1), { type: 'cancel' })
  // Deafened audio still flows to a silent output, so receipts keep their timing.
  call.toggleDeafen()
  call.toggleDeafen()
  assert.deepEqual(audio.deafened, [false, true, false])
})

test("the player's receipts tell the engine what the owner heard", async () => {
  const { ws, audio } = await liveCall()
  audio.receipts.started('r1')
  audio.receipts.ended('r1', 812.4)
  audio.receipts.cancelled('r2', 'clear', 0)
  assert.deepEqual(ws.sent.slice(-3), [
    { type: 'playback', id: 'r1', state: 'started', played_ms: 0 },
    { type: 'playback', id: 'r1', state: 'ended', played_ms: 812 },
    { type: 'playback', id: 'r2', state: 'ended', played_ms: 0 },
  ])
})

test('the socket close says how the call ended', async () => {
  let t = await liveCall()
  t.ws.drop(4001, 'taken over')
  assert.equal(t.call.state.phase, 'error')
  assert.match(t.call.state.error, /open in another window/)
  assert.equal(t.audio.closed, 1)
  t = await liveCall()
  t.ws.drop(1011, 'the voice service lost OpenAI')
  assert.equal(t.call.state.phase, 'error')
  assert.equal(t.call.state.error, 'the voice service lost OpenAI')
  // A normal end (Hang up elsewhere, or 60 s with no page) is no error.
  t = await liveCall()
  t.ws.drop(1000)
  assert.equal(t.call.state.phase, 'idle')
})

test('a start refused for another room names that room', async () => {
  const { call, calls } = setup({ startError: 'a voice call is already live in room a; end it first', status: { live: true, roomId: 'room_a', room: 'a' } })
  await call.start('room_1', 'voice-test')
  assert.equal(call.state.phase, 'error')
  assert.equal(call.state.busyRoom, 'a')
  assert.equal(call.state.error, 'Voice is in a call in a.')
  assert.ok(!calls.some(([k]) => k === 'socket'))
})

test('a refused microphone never starts the gateway', async () => {
  const { call, calls } = setup({ micError: Object.assign(new Error('denied'), { name: 'NotAllowedError' }) })
  await call.start('room_1', 'voice-test')
  assert.equal(call.state.phase, 'error')
  assert.match(call.state.error, /microphone/)
  assert.deepEqual(calls, [])
})

test('hang up tells the engine, closes the socket and the audio, and stops the call', async () => {
  const { call, ws, calls, audio } = await liveCall()
  await call.hangup()
  assert.deepEqual(ws.sent.at(-1), { type: 'hangup' })
  assert.equal(ws.closed, 1000)
  assert.equal(audio.closed, 1)
  assert.deepEqual(calls.at(-1), ['stop'])
  assert.equal(call.state.phase, 'idle')
  // The late close of the old socket changes nothing.
  ws.drop(1000)
  assert.equal(call.state.phase, 'idle')
})

test('mute is the turn signal: releasing push to talk or muting closes the turn, with no silent tail', async () => {
  const { call, ws, mic } = await liveCall()
  const sent = () => ws.sent.filter((m) => m.type === 'mute').map((m) => m.muted)
  call.setPushToTalk(true)
  call.talk(true)
  mic()
  call.talk(false)
  assert.deepEqual(sent(), [false, true, false, true])
  mic()
  assert.equal(ws.sent.filter((m) => m instanceof ArrayBuffer).length, 1, 'the mic stays closed after the release')
  call.setPushToTalk(false)
  call.toggleMute()
  call.toggleMute()
  assert.deepEqual(sent(), [false, true, false, true, false, true, false])
  // Nothing but audio frames carries sound.
  assert.ok(ws.sent.filter((m) => m instanceof ArrayBuffer).every((m) => new Uint8Array(m).some((byte) => byte !== 0)))
})

test("a failed start's late status never touches the next call", async () => {
  let status
  let starts = 0
  const ws = fakeSocket()
  const call = createCall({
    socket: () => ws,
    api: {
      voiceStart: () => (++starts === 1 ? Promise.reject(new Error('busy')) : new Promise(() => {})),
      voiceStatus: () => new Promise((resolve) => { status = resolve }),
      voiceStop: async () => {},
    },
    openAudio: async () => ({ play() {}, done() {}, stopPlayback() {}, setDeafened() {}, close() {} }),
  })
  const first = call.start('room_1', 'a')
  while (!status) await new Promise((r) => setImmediate(r))
  await call.hangup()
  call.start('room_1', 'a')
  status({ live: true, roomId: 'room_2', room: 'b' })
  await first
  assert.equal(call.state.phase, 'starting')
  assert.equal(call.state.busyRoom, '')
})

test('unmute after deafen turns the output back on', async () => {
  const { call, audio } = await liveCall()
  call.toggleDeafen()
  call.toggleMute()
  assert.equal(call.state.deafened, false)
  // Set at start, silenced by deafen, restored by unmute.
  assert.deepEqual(audio.deafened, [false, true, false])
})

test('Call Voice in another room asks first, then ends that call and calls here', async () => {
  const t = await liveCall()
  t.call.call('room_2', 'b')
  assert.equal(t.call.state.phase, 'live', 'the call goes on until the owner confirms')
  assert.deepEqual(t.call.state.switchTo, { roomId: 'room_2', room: 'b' })
  await t.call.switchHere('room_2', 'b')
  assert.equal(t.ws.closed, 1000)
  assert.deepEqual(t.calls.slice(-3), [['stop'], ['start', 'room_2'], ['socket', 'b']])
  assert.equal(t.call.state.roomId, 'room_2')
  assert.equal(t.call.state.switchTo, null)
})

test('refresh turns the header on once voice is set up', async () => {
  let available = false
  const t = setup()
  const call = createCall({ ...t.deps, api: { ...t.deps.api, voiceStatus: async () => ({ available }) } })
  await call.refresh()
  assert.equal(call.state.available, false)
  available = true
  await call.refresh()
  assert.equal(call.state.available, true)
})

test("an old call's socket never touches the next call", async () => {
  const sockets = []
  const t = setup()
  const call = createCall({ ...t.deps, socket: () => { const ws = fakeSocket(); sockets.push(ws); return ws } })
  await call.start('room_1', 'a')
  const old = sockets[0]
  old.open()
  await call.hangup()
  await call.start('room_1', 'a')
  const next = sockets[1]
  const sentBefore = old.sent.length
  // Events still queued on the old socket arrive after the new call began.
  old.onopen()
  old.recv({ type: 'state', state: 'listening' })
  old.recv({ type: 'audio', id: 'r1', audio: 'AAAA' })
  old.recv({ type: 'clear', id: 'r1' })
  old.drop(1006)
  assert.equal(call.state.phase, 'connecting')
  assert.equal(t.audio.played.length, 0)
  assert.deepEqual(t.audio.stops, [])
  assert.equal(old.sent.length, sentBefore, 'nothing more goes out on the old socket')
  next.open()
  next.recv({ type: 'state', state: 'listening' })
  assert.equal(call.state.phase, 'live')
  assert.deepEqual(next.sent[0], { type: 'mute', muted: false })
})

// The owner may talk as soon as they press Call. Their words wait in the
// browser until the call's socket opens; the engine holds them until Voice
// is ready, and they are never lost.
const spoken = (ws) => ws.sent.filter((m) => m instanceof ArrayBuffer)

test('speech after Call and before the socket opens is sent as it opens', async () => {
  const t = setup()
  await t.call.start('room_1', 'voice-test')
  t.mic()
  t.mic()
  assert.equal(spoken(t.ws).length, 0, 'nothing goes out before the socket opens')
  t.ws.open()
  const sent = spoken(t.ws).reduce((n, m) => n + m.byteLength, 0)
  // Two frames of 480 samples at 48 kHz are 480 samples at 24 kHz: 960 bytes.
  assert.equal(sent, 960)
  // Then frames stream at once, before Voice is ready: the engine holds them.
  t.mic()
  assert.equal(spoken(t.ws).length, 2)
})

test('hanging up drops speech that waited for Voice', async () => {
  const t = setup()
  await t.call.start('room_1', 'voice-test')
  t.mic()
  await t.call.hangup()
  // A late frame from the closed microphone after the hang-up.
  t.mic()
  await t.call.start('room_1', 'voice-test')
  t.ws.open()
  assert.equal(spoken(t.ws).length, 0)
})

test('a failed start drops speech that waited for Voice', async () => {
  const t = setup()
  const ok = t.deps.api.voiceStart
  let fail
  t.deps.api.voiceStart = () => new Promise((_, reject) => { fail = reject })
  const starting = t.call.start('room_1', 'voice-test')
  await new Promise((r) => setImmediate(r))
  t.mic()
  fail(new Error('no gateway'))
  await starting
  assert.equal(t.call.state.phase, 'error')
  t.deps.api.voiceStart = ok
  t.call.dismiss()
  await t.call.start('room_1', 'voice-test')
  t.ws.open()
  assert.equal(spoken(t.ws).length, 0)
})

test('speech while muted never waits for Voice', async () => {
  const t = setup()
  await t.call.start('room_1', 'voice-test')
  t.call.toggleMute()
  t.mic()
  t.call.toggleMute()
  t.ws.open()
  assert.equal(spoken(t.ws).length, 0, 'muted speech is not kept')
})

test('push to talk speech released before the socket opens still reaches Voice, and closes the turn', async () => {
  const t = setup()
  t.call.setPushToTalk(true)
  await t.call.start('room_1', 'voice-test')
  t.call.talk(true)
  t.mic()
  t.call.talk(false)
  t.mic() // not held: not kept
  t.ws.open()
  // One 480-sample frame at 48 kHz is 240 samples at 24 kHz: 480 bytes.
  assert.equal(spoken(t.ws).reduce((n, m) => n + m.byteLength, 0), 480)
  // The speech goes first, then the closed mic ends the turn.
  assert.deepEqual(t.ws.sent.at(-1), { type: 'mute', muted: true })
})

// Voice ducks while the owner talks to it, and only then.
test('only frames Voice hears drive ducking, and stopping capture unducks', async () => {
  const t = await liveCall()
  t.mic()
  assert.equal(t.audio.heard, 1)
  t.call.toggleMute()
  assert.equal(t.audio.unducked, 1)
  t.mic()
  assert.equal(t.audio.heard, 1, 'muted speech never ducks Voice')
})

test('a page load shows a live call to resume, with no mic and no socket until Rejoin', async () => {
  let opened = 0
  let stopped = 0
  const t = setup()
  const call = createCall({
    ...t.deps,
    api: {
      ...t.deps.api,
      voiceStatus: async () => (stopped ? { available: true, live: false } : { available: true, live: true, roomId: 'room_1', room: 'voice-test', startedAt: 500 }),
      voiceStop: async () => { stopped++ },
    },
    openAudio: async (...args) => { opened++; return t.deps.openAudio(...args) },
  })
  await call.refresh()
  assert.equal(call.state.phase, 'resumable')
  assert.equal(opened, 0, 'a page load must never open the microphone')
  assert.ok(!t.calls.some(([k]) => k === 'socket' || k === 'start'))
  // Rejoin: the engine returns the same call, and the page connects.
  await call.start('room_1', 'voice-test')
  assert.equal(opened, 1)
  assert.deepEqual(t.calls, [['start', 'room_1'], ['socket', 'voice-test']])
  assert.equal(call.state.phase, 'connecting')
})

test('End stops a call found on page load, and Rejoin waits until it has ended', async () => {
  let live = true
  let finish
  const t = setup()
  const call = createCall({
    ...t.deps,
    api: {
      ...t.deps.api,
      voiceStatus: async () => (live ? { available: true, live: true, roomId: 'room_1', room: 'voice-test', startedAt: 500 } : { available: true, live: false }),
      voiceStop: () => new Promise((resolve) => { finish = () => { live = false; resolve() } }),
    },
  })
  await call.refresh()
  const ending = call.hangup() // End
  assert.equal(call.state.phase, 'ending')
  await call.start('room_1', 'voice-test') // Rejoin while it ends
  assert.ok(!t.calls.some(([k]) => k === 'start'), 'no call starts during the stop')
  assert.equal(t.audio.receipts, null, 'no microphone opens during the stop')
  finish()
  await ending
  assert.equal(call.state.phase, 'idle')
})

test('a call found on page load that ends elsewhere leaves the resume state', async () => {
  let live = true
  const timers = []
  const t = setup()
  const call = createCall({
    ...t.deps,
    every: (ms, fn) => { const timer = { ms, fn, on: true }; timers.push(timer); return () => { timer.on = false } },
    api: { ...t.deps.api, voiceStatus: async () => (live ? { available: true, live: true, roomId: 'room_1', room: 'voice-test', startedAt: 500 } : { available: true, live: false }) },
  })
  await call.refresh()
  assert.equal(call.state.phase, 'resumable')
  assert.equal(timers.length, 1)
  live = false // 60 s without a page: the engine ended it
  await timers[0].fn()
  assert.equal(call.state.phase, 'idle')
  assert.equal(timers[0].on, false, 'it stops asking once the call is gone')
})
