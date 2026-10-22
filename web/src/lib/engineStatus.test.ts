import assert from 'node:assert/strict'
import { test } from 'node:test'
import { diagnose, engineWatch, type EngineIO, type EngineStatus } from './engineStatus.ts'

// Fake is an EngineIO the test drives by hand. check answers at once.
class Fake implements EngineIO {
  onLine = true
  checks = 0
  local = false
  answer: number | 'network'
  fire: () => void = () => {}
  constructor(answer: number | 'network') {
    this.answer = answer
  }
  online = () => this.onLine
  check = () => {
    this.checks++
    return Promise.resolve(this.answer)
  }
  listen = (fn: () => void) => {
    this.fire = fn
    return () => (this.fire = () => {})
  }
}

// settle lets a resolved check finish; setImmediate is not mocked.
const settle = () => new Promise((ok) => setImmediate(ok))

test('diagnose blames the engine only when it answered with an error, or on this computer', async () => {
  const io = (answer: number | 'network', local = false, onLine = true): EngineIO => ({
    online: () => onLine,
    check: () => Promise.resolve(answer),
    local,
    listen: () => () => {},
  })
  assert.equal(await diagnose(io(502, false, false)), 'offline')
  assert.equal(await diagnose(io('network')), 'unreachable', 'a phone without a route is not told to run systemctl')
  assert.equal(await diagnose(io('network', true)), 'engine', 'on this computer only the engine can be down')
  assert.equal(await diagnose(io(502)), 'engine', 'the relay answers 502 when the engine is gone')
  assert.equal(await diagnose(io(404)), 'unreachable', 'a server that is not the engine answered')
  assert.equal(await diagnose(io(403)), 'unreachable')
  assert.equal(await diagnose(io(401)), null, 'a signed-out device goes to the pairing screen')
  assert.equal(await diagnose(io(200)), '')
})

test('the banner says reconnecting at once and names the reason after the grace period', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const seen: EngineStatus[] = []
  const io = new Fake('network')
  const engine = engineWatch((s) => seen.push(s), io, 5000)
  engine.state(false)
  assert.deepEqual(seen, ['reconnecting'])
  t.mock.timers.tick(4999)
  assert.equal(io.checks, 0, 'a short restart is not diagnosed')
  t.mock.timers.tick(1)
  await settle()
  assert.deepEqual(seen, ['reconnecting', 'unreachable'])
  io.answer = 503
  t.mock.timers.tick(5000)
  await settle()
  assert.deepEqual(seen, ['reconnecting', 'unreachable', 'engine'], 'the reason is checked again while down')
  engine.state(true)
  assert.deepEqual(seen, ['reconnecting', 'unreachable', 'engine', ''])
  t.mock.timers.tick(20000)
  await settle()
  assert.equal(seen.length, 4, 'nothing is checked once the stream is back')
  engine.stop()
})

test('going offline shows offline at once; coming back online reconnects', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const seen: EngineStatus[] = []
  const io = new Fake(200)
  const engine = engineWatch((s) => seen.push(s), io, 5000)
  io.onLine = false
  io.fire()
  assert.deepEqual(seen, [], 'a live stream ignores the offline event')
  engine.state(false)
  assert.deepEqual(seen, ['offline'])
  io.onLine = true
  io.fire()
  assert.deepEqual(seen, ['offline', 'reconnecting'])
  io.onLine = false
  io.fire()
  assert.deepEqual(seen, ['offline', 'reconnecting', 'offline'])
  engine.stop()
  io.fire()
  t.mock.timers.tick(10000)
  await settle()
  assert.equal(seen.length, 3, 'stop ends the watch')
})

test('a signed-out device stops the checks', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const seen: EngineStatus[] = []
  const io = new Fake(401)
  const engine = engineWatch((s) => seen.push(s), io, 5000)
  engine.state(false)
  t.mock.timers.tick(5000)
  await settle()
  t.mock.timers.tick(20000)
  await settle()
  assert.equal(io.checks, 1, 'no probe repeats the sign-in handling')
  assert.deepEqual(seen, ['reconnecting'])
  engine.stop()
})

test('a diagnosis that lands after the stream is back changes nothing', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const seen: EngineStatus[] = []
  let answer: (v: number | 'network') => void = () => {}
  const io: EngineIO = {
    online: () => true,
    check: () => new Promise((ok) => (answer = ok)),
    local: false,
    listen: () => () => {},
  }
  const engine = engineWatch((s) => seen.push(s), io, 5000)
  engine.state(false)
  t.mock.timers.tick(5000)
  engine.state(true)
  answer(502)
  await settle()
  assert.deepEqual(seen, ['reconnecting', ''])
  engine.stop()
})
