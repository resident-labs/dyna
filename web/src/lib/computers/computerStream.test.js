import test from 'node:test'
import assert from 'node:assert/strict'
import { autoQuality, savedStreamMode, setEncodingMode, requestedEncodings } from './computerStream.js'

test('Auto is the default; explicit Sharp and Smooth stay selected', () => {
  assert.equal(savedStreamMode(null), 'auto')
  assert.equal(savedStreamMode('bad'), 'auto')
  assert.equal(savedStreamMode('sharp'), 'sharp')
  assert.equal(savedStreamMode('smooth'), 'smooth')
})

test('Auto starts Smooth, sharpens after 1.5 seconds, and needs sustained motion to return', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1499)
  assert.deepEqual(changes, [])
  q.sample(0, 1400)
  q.sample(0, 1500)
  assert.deepEqual(changes, [false])
  q.sample(0.2, 1600)
  q.sample(0, 1700)
  q.sample(0, 1901)
  assert.deepEqual(changes, [false], 'one changed sample from text refinement does not switch')
  q.sample(0.2, 2000)
  q.sample(0.2, 2100)
  assert.deepEqual(changes, [false, true])
  q.sample(0, 3599)
  assert.deepEqual(changes, [false, true])
  q.sample(0, 3600)
  assert.deepEqual(changes, [false, true, false])
})

test('cursor-sized changes and motion below the threshold do not flap', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  for (let t = 100; t <= 3000; t += 100) q.sample(0.003, t)
  assert.deepEqual(changes, [false])
  for (let t = 3100; t <= 5000; t += 100) q.sample(0.2, t)
  assert.deepEqual(changes, [false, true])
})

test('live encoding choice belongs to each socket and refreshes Sharp without reconnecting', () => {
  const refresh = []
  class FakeRFB {
    static messages = { fbUpdateRequest: (...args) => refresh.push(args) }
    constructor() { this._rfbConnectionState = 'connected'; this._sock = {}; this._fbWidth = 1280; this._fbHeight = 800; this.sent = [] }
    _sendEncodings() { this.sent.push(requestedEncodings(this._sock, [1, 50, 7])) }
    disconnect() { assert.fail('must keep the connection') }
  }
  const a = new FakeRFB(), b = new FakeRFB()
  setEncodingMode(a, true)
  setEncodingMode(b, false)
  assert.deepEqual(a.sent.at(-1), [1, 50, 7])
  assert.deepEqual(b.sent.at(-1), [1, 7])
  a._sendEncodings()
  assert.deepEqual(a.sent.at(-1), [1, 50, 7])
  assert.equal(b.qualityLevel, 9)
  assert.equal(b.compressionLevel, 4)
  assert.deepEqual(refresh.at(-1), [b._sock, false, 0, 0, 1280, 800])
})

test('Sharp redraws cannot immediately trigger a switch back to Smooth', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1400)
  q.sample(0, 1500)
  q.sample(0.2, 1600)
  q.sample(0.2, 1700)
  assert.deepEqual(changes, [false], 'ignore the first 300 ms while Sharp refreshes')
  q.sample(0.2, 1800)
  q.sample(0.2, 1900)
  assert.deepEqual(changes, [false, true], 'sustained motion after the refresh still switches')
})

async function withStream(run) {
  const { build } = await import('esbuild')
  const { mkdtemp, writeFile, rm } = await import('node:fs/promises')
  const { tmpdir } = await import('node:os')
  const { join } = await import('node:path')
  const dir = await mkdtemp(join(tmpdir(), 'dyna-stream-test-'))
  const names = ['window', 'document', 'WebSocket', 'startupReady']
  const previous = names.map(n => [n, Object.getOwnPropertyDescriptor(globalThis, n)])
  const instances = [], sockets = []
  let ready, stream
  const firstReady = new Promise(resolve => { ready = resolve })
  globalThis.startupReady = instance => { instances.push(instance); ready(instance) }
  globalThis.window = { addEventListener() {}, removeEventListener() {} }
  globalThis.document = { createElement: () => ({ getContext: () => null }) }
  globalThis.WebSocket = class {
    constructor() { this.listeners = {}; sockets.push(this) }
    addEventListener(name, fn) { this.listeners[name] = fn }
    emitClose(code) { this.listeners.close?.({ code }) }
    send() {}
    close() {}
  }
  try {
    const result = await build({
      entryPoints: [new URL('./computerStream.js', import.meta.url).pathname],
      bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent',
      plugins: [{ name: 'fake-rfb', setup(builder) {
        builder.onResolve({ filter: /^@novnc\/novnc$/ }, () => ({ path: 'rfb', namespace: 'fake' }))
        builder.onLoad({ filter: /.*/, namespace: 'fake' }, () => ({
          contents: `export default class RFB {
            static messages = { clientEncodings() {}, fbUpdateRequest() {} }
            constructor() {
              this._sock = {}; this._display = { flip() {}, pending: () => false };
              this._decoders = {}; this.listeners = {}; this._rfbConnectionState = 'connecting';
            }
            addEventListener(name, fn) {
              this.listeners[name] = fn;
              if (name === 'credentialsrequired') globalThis.startupReady(this);
            }
            emit(name, detail = {}) {
              if (name === 'connect') this._rfbConnectionState = 'connected';
              if (name === 'disconnect') this._rfbConnectionState = 'disconnected';
              this.listeners[name]?.({ detail });
            }
            _sendEncodings() {}
            focus() {}
            blur() {}
            disconnect() { this.disconnectCalls = (this.disconnectCalls || 0) + 1; this.emit('disconnect', { clean: true }) }
          }`,
          loader: 'js',
        }))
      } }],
    })
    const file = join(dir, 'stream.mjs')
    await writeFile(file, result.outputFiles[0].contents)
    const mod = await import(file)
    const target = { replaceChildren() {}, addEventListener() {}, removeEventListener() {} }
    await run({ mod, instances, sockets, firstReady, open(options = {}) {
      return stream = mod.openStream({ target, mode: 'sharp', url: async () => 'ws://example.test', on() {}, ...options })
    } })
  } finally {
    stream?.close()
    for (const [name, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor)
      else delete globalThis[name]
    }
    await rm(dir, { recursive: true, force: true })
  }
}

test('a mode chosen while the URL is pending applies to the new connection', async () => {
  await withStream(async f => {
    let enterURL, resolveURL
    const entered = new Promise(resolve => { enterURL = resolve })
    const url = new Promise(resolve => { resolveURL = resolve })
    const stream = f.open({ mode: 'auto', url: () => { enterURL(); return url } })
    await entered
    stream.setMode('sharp')
    resolveURL('ws://example.test')
    await f.firstReady
    assert.equal(stream.rfb.qualityLevel, 9)
    assert.equal(stream.rfb.compressionLevel, 4)
    assert.deepEqual(f.mod.requestedEncodings(stream.rfb._sock, [50, 7]), [7])
    const display = stream.rfb._display
    display.pending = () => true
    display.flip()
    assert.equal(stream.meter.frames.length, 0, 'queued flips do not count as painted frames')
    display.flip(true)
    assert.equal(stream.meter.frames.length, 1, 'painting the queued frame counts once')
    display.pending = () => false
    display.flip()
    assert.equal(stream.meter.frames.length, 2)
  })
})

test('an abnormal socket close retries even when noVNC calls it clean', async t => {
  await withStream(async f => {
    t.mock.timers.enable({ apis: ['setTimeout'] })
    const states = []
    const stream = f.open({ on: (state, detail) => states.push({ state, detail }) })
    const first = await f.firstReady
    first.emit('connect')
    stream.setControl(true)
    f.sockets[0].emitClose(1006)
    first.emit('disconnect', { clean: true })
    t.mock.timers.tick(1000)
    await new Promise(setImmediate)
    assert.equal(f.instances.length, 2)
    assert.equal(first.disconnectCalls || 0, 0, 'an ended RFB is not disconnected twice')
    assert.equal(f.instances[1].viewOnly, true, 'a new lease starts with agent control')
    assert.equal(states.at(-1).state, 'connecting')
    f.instances[1].emit('connect')
    assert.equal(states.at(-1).state, 'connected')
    stream.close()
    t.mock.timers.tick(20000)
    await new Promise(setImmediate)
    assert.equal(f.instances.length, 2, 'closing the viewer cancels retries')
  })
})

test('normal closes and refused credentials do not retry', async t => {
  for (const refused of [false, true]) {
    await withStream(async f => {
      t.mock.timers.enable({ apis: ['setTimeout'] })
      const states = []
      f.open({ on: state => states.push(state) })
      const first = await f.firstReady
      first.emit('connect')
      if (refused) first.emit('securityfailure', { reason: 'access denied' })
      f.sockets[0].emitClose(refused ? 1006 : 1000)
      first.emit('disconnect', { clean: true })
      t.mock.timers.tick(20000)
      await new Promise(setImmediate)
      assert.equal(f.instances.length, 1)
      assert.equal(states.at(-1), refused ? 'failed' : 'disconnected')
      t.mock.timers.reset()
    })
  }
})

test('closing during URL lookup prevents a later connection', async () => {
  await withStream(async f => {
    let enterURL, resolveURL
    const entered = new Promise(resolve => { enterURL = resolve })
    const url = new Promise(resolve => { resolveURL = resolve })
    const stream = f.open({ url: () => { enterURL(); return url } })
    await entered
    stream.close()
    resolveURL('ws://example.test')
    await new Promise(setImmediate)
    assert.equal(f.instances.length, 0)
  })
})

test('events from a replaced RFB cannot change the current phase', async () => {
  await withStream(async f => {
    const states = []
    const stream = f.open({ on: state => states.push(state) })
    const first = await f.firstReady
    first.emit('connect')
    stream.setControl(true)
    stream.reconnect()
    await new Promise(setImmediate)
    assert.equal(f.instances.length, 2)
    assert.equal(f.instances[1].viewOnly, true, 'manual reconnect also starts a new lease')
    first.emit('connect')
    assert.equal(states.at(-1), 'connecting')
    f.instances[1].emit('connect')
    assert.equal(states.at(-1), 'connected')
  })
})

test('scroll input keeps Auto Smooth even when near-identical lines change few pixels', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1400)
  q.sample(0, 1500)
  q.scroll(1600)
  assert.deepEqual(changes, [false, true], 'direct scroll input can interrupt the redraw guard')
  q.scroll(2600)
  q.sample(0.001, 4099)
  assert.deepEqual(changes, [false, true])
  q.sample(0, 4100)
  assert.deepEqual(changes, [false, true, false])
})

test('large scroll-shaped damage detects motion when sampled pixels look alike', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1400)
  q.sample(0, 1500)
  // A tiled update spans most of a terminal, with near-identical text.
  for (const time of [1900, 2000]) {
    q.region(0, 100, 640, 400, 1280, 800)
    q.region(640, 100, 640, 400, 1280, 800)
    q.sample(0.001, time)
  }
  assert.deepEqual(changes, [false, true])
  q.sample(0, 3400)
  q.sample(0, 3500)
  q.region(10, 10, 16, 16, 1280, 800)
  q.sample(0.001, 3900)
  q.region(10, 10, 16, 16, 1280, 800)
  q.sample(0.001, 4000)
  assert.deepEqual(changes, [false, true, false], 'cursor damage does not count')
})

test('full-frame H.264 regions from a blinking caret still let Auto sharpen', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  for (let time = 100; time <= 3000; time += 100) {
    if (time % 500 === 0) q.region(0, 0, 1280, 800, 1280, 800)
    q.sample(0.001, time)
  }
  assert.deepEqual(changes, [false])
})

test('a tall strip of changed row numbers detects near-identical terminal scrolling', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1400)
  q.sample(0, 1500)
  for (const time of [1900, 2000]) {
    q.region(0, 0, 48, 750, 1280, 800)
    q.sample(0.001, time)
  }
  assert.deepEqual(changes, [false, true])
})

test('a delayed Sharp refresh is ignored until its frame has finished painting', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1400)
  q.sample(0, 1500)
  q.refreshing()
  q.sample(0.2, 2300)
  q.sample(0.2, 2400)
  assert.deepEqual(changes, [false])
  q.refreshed(2500)
  q.sample(0.2, 2600)
  q.sample(0.2, 2700)
  assert.deepEqual(changes, [false])
  q.sample(0.2, 2800)
  q.sample(0.2, 2900)
  assert.deepEqual(changes, [false, true])
})

test('a blocked renderer gets a fresh quiet sample before requesting Sharp', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 100)
  q.sample(0, 4000)
  assert.deepEqual(changes, [], 'a long gap is not evidence of a still picture')
  q.scroll(4001)
  q.sample(0, 4100)
  assert.deepEqual(changes, [], 'queued scroll input keeps Smooth')
  q.sample(0, 5401)
  q.sample(0, 5501)
  assert.deepEqual(changes, [false])
})

test('Auto keeps Smooth after a slow frame, until the mode or connection is reset', () => {
  const changes = []
  const q = autoQuality(on => changes.push(on), 0)
  q.sample(0, 1400)
  q.sample(0, 1500)
  q.keepSmooth()
  assert.deepEqual(changes, [false, true])
  for (let time = 1600; time <= 8000; time += 100) q.sample(0, time)
  assert.deepEqual(changes, [false, true], 'still text does not request Sharp again')
  const fresh = autoQuality(on => changes.push(on), 9000)
  fresh.sample(0, 10400)
  fresh.sample(0, 10500)
  assert.deepEqual(changes, [false, true, false], 'a fresh mode or connection can try Sharp')
})

test('a replaced URL lookup cannot report a late failure over a live stream', async () => {
  await withStream(async f => {
    let enterURL, rejectURL, calls = 0
    const entered = new Promise(resolve => { enterURL = resolve })
    const oldURL = new Promise((resolve, reject) => { rejectURL = reject })
    const states = []
    const stream = f.open({
      url: () => { if (calls++ === 0) { enterURL(); return oldURL } return Promise.resolve('ws://example.test') },
      on: state => states.push(state),
    })
    await entered
    stream.reconnect()
    await f.firstReady
    stream.rfb.emit('connect')
    rejectURL(new Error('old request failed'))
    await new Promise(setImmediate)
    assert.equal(states.at(-1), 'connected')
  })
})
