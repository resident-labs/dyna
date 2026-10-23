import test from 'node:test'
import assert from 'node:assert/strict'
import { Meter, statsLine, ENCODING } from './rfbStats.ts'

function clock() {
  let t = 0
  return { now: () => t, tick: (ms: number) => (t += ms) }
}

test('fps and bitrate use the last two seconds', () => {
  const c = clock()
  const m = new Meter(c.now)
  for (let i = 0; i < 60; i++) { m.frame(); m.received(25000); c.tick(33) }
  const s = m.snapshot()
  assert.equal(s.fps, 30)
  assert.ok(Math.abs(s.mbit - 6) < 0.3, `got ${s.mbit}`)
  c.tick(5000)
  const quiet = m.snapshot()
  assert.equal(quiet.fps, 0)
  assert.equal(quiet.mbit, 0)
})

test('the codec is the encoding with the most rectangles, and CopyRect does not count', () => {
  const c = clock()
  const m = new Meter(c.now)
  m.rect(1); m.rect(1); m.rect(1)
  assert.equal(m.snapshot().codec, '')
  m.rect(7); m.rect(50); m.rect(50)
  assert.equal(m.snapshot().codec, 'H.264')
  c.tick(3000)
  m.rect(16)
  assert.equal(m.snapshot().codec, 'ZRLE', 'old rectangles age out')
  m.rect(999)
  c.tick(10)
  assert.equal(new Meter(c.now).snapshot().codec, '')
  assert.equal(ENCODING[7], 'Tight')
})

test('latency is the delay from input to the next reply and goes stale', () => {
  const c = clock()
  const m = new Meter(c.now)
  m.reply()
  assert.equal(m.snapshot().latency, null)
  m.sent(); c.tick(16); m.reply()
  assert.equal(m.snapshot().latency, 16)
  m.reply()
  assert.equal(m.snapshot().latency, 16, 'a second reply without input changes nothing')
  c.tick(11000)
  assert.equal(m.snapshot().latency, null)
})

test('stats line', () => {
  const snap = { fps: 30, mbit: 0.94, codec: '', latency: null }
  assert.deepEqual(statsLine(snap, { stream: { codec: 'H.264', encoder: 'VA-API' } }), ['codec —', 'encoder VA-API', '30 fps', '0.9 Mbit/s', 'latency —'])
  assert.deepEqual(statsLine({ ...snap, codec: 'Tight', mbit: 14.2, latency: 16 }, {}), ['codec Tight', '30 fps', '14 Mbit/s', 'latency 16 ms'])
  assert.equal(statsLine(snap, undefined)[0], 'codec —')
})

test('a long unfinished frame with low throughput marks a slow link', () => {
  const c = clock()
  const m = new Meter(c.now)
  m.received(65536)
  c.tick(500)
  m.received(65536)
  assert.equal(m.slowFrame(), false, 'wait for a full second of transfer')
  c.tick(501)
  assert.equal(m.slowFrame(), true)
  m.frame()
  assert.equal(m.slowFrame(), false, 'a painted frame ends the transfer')
  m.received(16)
  c.tick(2000)
  assert.equal(m.slowFrame(), false, 'small protocol messages and idle are not congestion')
})

test('fast transfers and regular frames do not mark a slow link', () => {
  const c = clock()
  const m = new Meter(c.now)
  m.received(2000000)
  c.tick(1001)
  assert.equal(m.slowFrame(), false, 'high throughput alone does not block Sharp')
  m.resetFrame()
  for (let i = 0; i < 20; i++) {
    m.received(65536)
    c.tick(100)
    m.frame()
    assert.equal(m.slowFrame(), false)
  }
  c.tick(5000)
  assert.equal(m.slowFrame(), false)
})

test('idle protocol bytes cannot make the next fast frame look slow', () => {
  const c = clock()
  const m = new Meter(c.now)
  m.received(16)
  c.tick(5000)
  m.received(65536)
  assert.equal(m.slowFrame(), false, 'the transfer clock starts with substantial frame data')
  c.tick(1000)
  assert.equal(m.slowFrame(), true, 'a genuinely unfinished transfer still trips the guard')
})
