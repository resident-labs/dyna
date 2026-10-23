import assert from 'node:assert/strict'
import { test } from 'node:test'
import { openStream, type StreamIO, type StreamMessage, type StreamSource } from './stream.ts'

// FakeSource is an EventSource the test drives by hand.
class FakeSource implements StreamSource {
  onopen: (() => void) | null = null
  onmessage: ((e: StreamMessage) => void) | null = null
  onerror: (() => void) | null = null
  readyState = 1
  listeners = new Map<string, (e: StreamMessage) => void>()
  readonly url: string
  constructor(url: string) {
    this.url = url
  }
  addEventListener(type: 'hello' | 'reset' | 'ping', fn: (e: StreamMessage) => void) {
    this.listeners.set(type, fn)
  }
  close() {
    this.readyState = 2
  }
  emit(type: 'hello' | 'reset' | 'ping', lastEventId = '', data: unknown = {}) {
    this.listeners.get(type)?.({ data: JSON.stringify(data), lastEventId })
  }
  message(data: unknown, lastEventId: string) {
    this.onmessage?.({ data: JSON.stringify(data), lastEventId })
  }
  fail() {
    this.readyState = 2
    this.onerror?.()
  }
}

function harness(poll: StreamIO['poll'] = () => new Promise(() => {})) {
  const sources: FakeSource[] = []
  const got: unknown[] = []
  const states: boolean[] = []
  let probes = 0
  let wake = () => {}
  const stop = openStream(
    (ev) => got.push(ev),
    (up) => states.push(up),
    {
      open: (url) => {
        const s = new FakeSource(url)
        sources.push(s)
        return s
      },
      poll,
      probe: () => probes++,
      wake: (fn) => {
        wake = fn
        return () => (wake = () => {})
      },
    },
  )
  const last = () => {
    const s = sources.at(-1)
    assert.ok(s)
    return s
  }
  return { sources, got, states, stop, last, probes: () => probes, wake: () => wake() }
}

test('waking reopens a lost stream at once instead of waiting out the backoff', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] })
  const h = harness()
  h.last().emit('hello', '5:1')
  h.wake()
  assert.equal(h.sources.length, 1, 'a live stream that was heard from stays')
  h.last().fail()
  t.mock.timers.tick(3000)
  h.last().fail()
  // The backoff is now 6 seconds; the phone comes back online after 1.
  t.mock.timers.tick(1000)
  h.wake()
  assert.equal(h.sources.length, 3)
  assert.equal(h.last().url, '/api/events?last=5%3A1')
  h.last().emit('hello')
  t.mock.timers.tick(10000)
  assert.equal(h.sources.length, 3, 'the cancelled retry does not open a fourth stream')
  assert.deepEqual(h.states, [false, true])
  h.stop()
  h.wake()
  assert.equal(h.sources.length, 3, 'a stopped stream ignores wake')
})

test('waking replaces a stream whose connection failed, even inside the silence window', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] })
  const h = harness()
  h.last().emit('hello', '5:1')
  // A network drop leaves the source CONNECTING with an error, so the last
  // traffic is recent and the old guard would keep the dead source.
  h.last().readyState = 0
  h.last().onerror?.()
  t.mock.timers.tick(1000)
  h.wake()
  assert.equal(h.sources[0]?.readyState, 2, 'the failed source is closed')
  assert.equal(h.sources.length, 2, 'the wake opens a fresh stream at once')
  assert.equal(h.last().url, '/api/events?last=5%3A1')
  h.stop()
})

test('waking replaces a stream that went silent while the page was hidden', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] })
  const h = harness()
  h.last().emit('hello', '5:1')
  // A hidden phone page runs no timers, but the clock still moves.
  t.mock.timers.setTime(Date.now() + 60000)
  h.wake()
  assert.equal(h.sources[0]?.readyState, 2, 'the silent stream is closed')
  assert.equal(h.sources.length, 2)
  assert.equal(h.last().url, '/api/events?last=5%3A1')
  assert.deepEqual(h.states, [false])
  h.stop()
})

test('a reconnect resumes from the last event ID without a resync', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = harness()
  assert.equal(h.last().url, '/api/events')
  h.last().emit('hello', '5:1')
  h.last().message({ kind: 'pins', roomId: 'r' }, '5:2')
  h.last().fail()
  assert.equal(h.probes(), 1)
  t.mock.timers.tick(3000)
  assert.equal(h.sources.length, 2)
  assert.equal(h.last().url, '/api/events?last=5%3A2')
  h.last().emit('hello')
  h.last().message({ kind: 'runs', roomId: 'r' }, '5:3')
  assert.deepEqual(h.got, [{ kind: 'pins', roomId: 'r' }, { kind: 'runs', roomId: 'r' }])
  assert.deepEqual(h.states, [false, true])
  h.stop()
})

test('a changed engine build on reconnect emits one build event', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = harness()
  h.last().emit('hello', '5:1', { version: 'build-a' })
  assert.deepEqual(h.got, [], 'the first hello sets the baseline')
  h.last().fail()
  t.mock.timers.tick(3000)
  h.last().emit('hello', '', { version: 'build-b' })
  h.last().emit('hello', '', { version: 'build-b' })
  assert.deepEqual(h.got, [{ kind: 'build', version: 'build-b' }])
  h.stop()
})

test('a changed engine build in long-poll mode emits one build event', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const replies: ((response: Response) => void)[] = []
  const h = harness(() => new Promise<Response>((resolve) => replies.push(resolve)))
  h.last().onopen?.()
  t.mock.timers.tick(6000)
  replies[0]?.(Response.json({ epoch: 5, seq: 1, version: 'build-a', events: [] }))
  await settle(() => replies.length > 1)
  assert.deepEqual(h.got, [{ kind: 'resync' }], 'the first poll sets the cursor and build baseline')
  replies[1]?.(Response.json({ epoch: 5, seq: 2, version: 'build-b', events: [] }))
  await settle(() => h.got.length > 1)
  assert.deepEqual(h.got, [{ kind: 'resync' }, { kind: 'build', version: 'build-b' }])
  h.stop()
})

test('a reset after a new epoch reloads once and moves the position', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = harness()
  h.last().emit('hello', '5:9')
  h.last().fail()
  t.mock.timers.tick(3000)
  h.last().emit('hello')
  h.last().emit('reset', '6:0')
  h.last().message({ kind: 'rooms' }, '6:1')
  assert.deepEqual(h.got, [{ kind: 'resync' }, { kind: 'rooms' }])
  h.last().fail()
  t.mock.timers.tick(6000)
  assert.equal(h.last().url, '/api/events?last=6%3A1')
  h.stop()
})

test('a stream silent for 30 seconds is replaced; a ping keeps it', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const h = harness()
  h.last().emit('hello', '5:1')
  t.mock.timers.tick(20000)
  h.last().emit('ping')
  t.mock.timers.tick(20000)
  assert.equal(h.sources.length, 1, 'a ping within 30 s keeps the stream')
  t.mock.timers.tick(10000)
  assert.equal(h.sources[0]?.readyState, 2, 'the silent stream is closed')
  t.mock.timers.tick(3000)
  assert.equal(h.sources.length, 2)
  assert.equal(h.last().url, '/api/events?last=5%3A1')
  assert.deepEqual(h.got, [])
  h.stop()
})

test('without a hello, the client long-polls from its last position', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const urls: string[] = []
  let answer: (r: Response) => void = () => {}
  const h = harness((url) => {
    urls.push(url)
    return new Promise((ok) => (answer = ok))
  })
  h.last().emit('hello', '5:4')
  h.last().fail()
  t.mock.timers.tick(3000)
  h.last().onopen?.()
  t.mock.timers.tick(6000)
  assert.deepEqual(urls, ['/api/events/poll?after=5%3A4'])
  answer(Response.json({ epoch: 5, seq: 5, events: [{ kind: 'pins' }] }))
  // The reply resolves through a few promise turns; setImmediate is not mocked.
  for (let i = 0; i < 20 && urls.length < 2; i++) await new Promise((ok) => setImmediate(ok))
  assert.deepEqual(h.got, [{ kind: 'pins' }])
  assert.equal(urls[1], '/api/events/poll?after=5%3A5')
  h.stop()
})

test('waking leaves a stream that is still connecting', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] })
  const h = harness()
  h.wake()
  assert.equal(h.sources.length, 1, 'the first stream waits for its hello')
  h.last().emit('hello', '5:1')
  h.last().fail()
  t.mock.timers.tick(3000)
  assert.equal(h.sources.length, 2)
  h.wake()
  assert.equal(h.sources.length, 2, 'a retry that just opened is not replaced')
  h.stop()
})

// settle lets awaited promises run; setImmediate is not mocked.
async function settle(done: () => boolean) {
  for (let i = 0; i < 20 && !done(); i++) await new Promise((ok) => setImmediate(ok))
}

test('in long-poll mode, waking ends the backoff and cuts short a poll in flight', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'] })
  const polls: { ok: (r: Response) => void; fail: (e: Error) => void }[] = []
  const h = harness((_url, signal) => new Promise((ok, fail) => {
    polls.push({ ok, fail })
    signal.addEventListener('abort', () => fail(new Error('aborted')))
  }))
  h.last().emit('hello', '5:4')
  h.last().fail()
  t.mock.timers.tick(3000)
  h.last().onopen?.()
  t.mock.timers.tick(6000)
  assert.equal(polls.length, 1)
  polls[0]?.fail(new Error('network'))
  await settle(() => false)
  // The backoff is now 6 seconds; the phone comes back online after 1.
  t.mock.timers.tick(1000)
  h.wake()
  await settle(() => polls.length > 1)
  assert.equal(polls.length, 2, 'the next poll starts at once')
  polls[1]?.ok(Response.json({ epoch: 5, seq: 5, events: [] }))
  await settle(() => polls.length > 2)
  assert.equal(polls.length, 3)
  const states = h.states.length
  h.wake()
  await settle(() => polls.length > 3)
  assert.equal(polls.length, 4, 'a wake replaces the poll in flight')
  assert.equal(h.states.length, states, 'cutting a poll short reports no loss')
  h.stop()
})
