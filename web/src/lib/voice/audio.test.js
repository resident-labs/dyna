import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createPlayer } from './audio.js'
import { encodeFrame } from './pcm.ts'

// fakeContext plays nothing; the test ends sources by hand.
function fakeContext() {
  const ctx = { currentTime: 0, sources: [] }
  ctx.createBuffer = (_, n, rate) => ({ duration: n / rate, getChannelData: () => new Float32Array(n) })
  ctx.createBufferSource = () => {
    const src = { connect() {}, start(at) { src.at = at }, stop() { src.stopped = true } }
    ctx.sources.push(src)
    return src
  }
  return ctx
}

const chunk = encodeFrame(new Float32Array(240).fill(0.1), 24000, 24000) // 10 ms

function setup() {
  const ctx = fakeContext()
  const got = []
  const player = createPlayer(ctx, {}, {
    started: (id) => got.push(['started', id]),
    ended: (id, ms) => got.push(['ended', id, Math.round(ms)]),
    cancelled: (id, reason, ms) => got.push(['cancelled', id, Math.round(ms)]),
  })
  return { ctx, got, player }
}

test('chunks play back to back; started once per response, ended after done and drained', () => {
  const { ctx, got, player } = setup()
  player.play(chunk, 24000, 'r1')
  player.play(chunk, 24000, 'r1')
  assert.deepEqual(ctx.sources.map((s) => s.at), [0, 0.01])
  assert.deepEqual(got, [['started', 'r1']])
  ctx.sources[0].onended()
  player.done('r1')
  assert.deepEqual(got, [['started', 'r1']], 'ended before the last chunk drained')
  ctx.sources[1].onended()
  // Two 10 ms chunks played in full.
  assert.deepEqual(got, [['started', 'r1'], ['ended', 'r1', 20]])
})

test('stop drops queued audio at once and cancels each response with the reason', () => {
  const { ctx, got, player } = setup()
  player.play(chunk, 24000, 'r1')
  player.play(chunk, 24000, 'r2')
  ctx.currentTime = 0.004
  player.stop('user_interruption')
  assert.ok(ctx.sources.every((s) => s.stopped && s.onended === null))
  // r1 played for 4 ms; r2 was queued and never started: 0 ms heard.
  assert.deepEqual(got, [['started', 'r1'], ['cancelled', 'r1', 4], ['cancelled', 'r2', 0]])
  // The next response starts now, not after the dropped queue.
  ctx.currentTime = 0.006
  player.play(chunk, 24000, 'r3')
  assert.equal(ctx.sources.at(-1).at, 0.006)
})

test('done for a response with no audio sends nothing', () => {
  const { got, player } = setup()
  player.done('r9')
  player.stop('')
  assert.deepEqual(got, [])
})

test("a response queued behind another reports started only when its audio starts", (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { ctx, got, player } = setup()
  player.play(chunk, 24000, 'r1')
  player.play(chunk, 24000, 'r1')
  player.play(chunk, 24000, 'r2') // starts at 20 ms, after r1
  assert.deepEqual(got, [['started', 'r1']])
  t.mock.timers.tick(19)
  assert.deepEqual(got, [['started', 'r1']], 'r2 is not heard yet')
  t.mock.timers.tick(1)
  assert.deepEqual(got, [['started', 'r1'], ['started', 'r2']])
  // r2 cannot end before it started, even if done and drained early.
  player.done('r2')
  ctx.sources[2].onended()
  assert.deepEqual(got.at(-1), ['ended', 'r2', 10])
})

test('barge-in drops a pending started receipt', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const { got, player } = setup()
  player.play(chunk, 24000, 'r1')
  player.play(chunk, 24000, 'r2')
  player.stop('user_interruption')
  t.mock.timers.tick(100)
  // r2 never played: the engine hears 0 ms for it.
  assert.deepEqual(got, [['started', 'r1'], ['cancelled', 'r1', 0], ['cancelled', 'r2', 0]])
})

// Exercise the real call, microphone and player wiring. Noise must not
// lower playback by default, and server interruption must still stop it.
test('ducking is opt-in and ignores background noise and short bursts', async (t) => {
  const contexts = []
  let mic
  const install = (name, value) => {
    const previous = Object.getOwnPropertyDescriptor(globalThis, name)
    Object.defineProperty(globalThis, name, { configurable: true, value })
    t.after(() => { if (previous) Object.defineProperty(globalThis, name, previous); else delete globalThis[name] })
  }
  install('navigator', { mediaDevices: { getUserMedia: async () => ({ getTracks: () => [{ stop() {} }] }) } })
  install('AudioWorkletNode', class { constructor() { this.port = {}; mic = this } })
  install('AudioContext', class {
    constructor() {
      Object.assign(this, fakeContext())
      this.sampleRate = 48000
      this.destination = {}
      this.audioWorklet = { addModule: async () => {} }
      this.gains = []
      contexts.push(this)
    }
    resume() {}
    close() {}
    createMediaStreamSource() { return { connect() {} } }
    createGain() {
      const gain = {
        value: 1,
        cancelScheduledValues() {},
        setTargetAtTime(value) { this.value = value },
        setValueAtTime(value) { this.value = value },
        linearRampToValueAtTime(value) { this.value = value },
      }
      this.gains.push(gain)
      return { gain, connect() {} }
    }
  })
  const { openAudio } = await import('./audio.js')
  const { createCall } = await import('./call.js')
  const start = async (ducking) => {
    const ws = { readyState: 0, send() {}, close() {} }
    const call = createCall({
      api: { voiceStart: async () => ({}), voiceStop: async () => {} },
      socket: () => ws,
      openAudio: (frame, receipts) => openAudio(frame, receipts, ducking),
    })
    await call.start('room_1', 'voice-test')
    ws.readyState = 1
    ws.onopen()
    const receive = (event) => ws.onmessage({ data: JSON.stringify(event) })
    receive({ type: 'state', state: 'speaking' })
    receive({ type: 'audio', id: 'r1', audio: chunk })
    return { call, receive, ctx: contexts.at(-1) }
  }
  const frame = (ctx, level, ms) => {
    ctx.currentTime += ms / 1000
    mic.port.onmessage({ data: new Float32Array(ms * ctx.sampleRate / 1000).fill(level) })
  }
  const off = await start(undefined)
  frame(off.ctx, 0.25, 200)
  assert.ok(off.ctx.gains.every((g) => g.value === 1), 'default playback was lowered by mic energy')
  off.receive({ type: 'clear', id: 'r1' })
  assert.ok(off.ctx.sources.every((s) => s.stopped), 'provider interruption did not stop playback')
  await off.call.hangup()

  const on = await start(true)
  frame(on.ctx, 0.04, 1000)
  frame(on.ctx, 0.2, 100)
  frame(on.ctx, 0.04, 100)
  assert.ok(on.ctx.gains.every((g) => g.value === 1), 'background noise or a brief burst lowered playback')
  frame(on.ctx, 0.2, 160)
  assert.ok(on.ctx.gains.some((g) => g.value < 1), 'sustained owner speech did not lower playback')
  await on.call.hangup()
})
