import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DUCKED, createDucker, type Ducker, type DuckGain } from './duck.ts'

// One recorded AudioParam call: the method name, then its numbers.
type Call = [name: string, ...args: number[]]

// A fake AudioParam that records what the ducker schedules.
function fakeGain(): DuckGain & { calls: Call[] } {
  const calls: Call[] = []
  return {
    calls,
    cancelScheduledValues: (t) => calls.push(['cancel', t]),
    setTargetAtTime: (v, t) => calls.push(['target', v, t]),
    setValueAtTime: (v, t) => calls.push(['set', v, t]),
    linearRampToValueAtTime: (v, t) => calls.push(['ramp', v, t]),
  }
}

const RATE = 1000
const speech = new Float32Array(40).fill(0.1) // 40 ms of speech
const quiet = new Float32Array(40).fill(0.001) // 40 ms of near silence

// feed gives the ducker frames back to back, from time 0.
function feed(ducker: Ducker, frames: Float32Array[]): number {
  let t = 0
  for (const f of frames) { t += f.length / RATE; ducker.hear(f, RATE, t) }
  return t
}

test('the owner speaking ducks Voice by 10 dB within one frame, as ChatGPT does', () => {
  const gain = fakeGain()
  const ducker = createDucker(gain)
  feed(ducker, [quiet, speech])
  assert.ok(Math.abs(DUCKED - 10 ** (-10 / 20)) < 1e-9)
  assert.deepEqual(gain.calls.find((c) => c[0] === 'target')?.slice(0, 2), ['target', DUCKED])
})

test('quiet frames and a single click never duck', () => {
  const gain = fakeGain()
  const ducker = createDucker(gain)
  const click = new Float32Array(40)
  click[0] = 1 // one loud sample: a low RMS
  feed(ducker, [quiet, click, quiet, quiet])
  assert.deepEqual(gain.calls, [])
})

test('after 800 ms of quiet Voice comes back: a 300 ms hold, then a 1 s ramp to full', () => {
  const gain = fakeGain()
  const ducker = createDucker(gain)
  // 1 s of speech, then 1 s of quiet.
  const t = feed(ducker, [...new Array<Float32Array>(25).fill(speech), ...new Array<Float32Array>(25).fill(quiet)])
  assert.ok(Math.abs(t - 2) < 1e-9)
  const set = gain.calls.find((c) => c[0] === 'set')
  const ramp = gain.calls.find((c) => c[0] === 'ramp')
  assert.ok(set && ramp, 'restored')
  // Quiet began at 1 s; release ends at 1.8 s; the hold ends at 2.1 s.
  assert.ok(Math.abs((set[2] ?? NaN) - 2.1) < 0.05, `hold ends at ${set[2]}`)
  assert.ok(Math.abs((ramp[2] ?? NaN) - (set[2] ?? NaN) - 1) < 1e-9)
  assert.equal(ramp[1], 1)
})

test('a short burst stays ducked for at least 750 ms', () => {
  const gain = fakeGain()
  const ducker = createDucker(gain)
  feed(ducker, [speech, ...new Array<Float32Array>(30).fill(quiet)])
  const set = gain.calls.find((c) => c[0] === 'set')
  assert.ok(set)
  assert.ok((set[2] ?? NaN) >= 0.04 + 0.75 - 1e-9)
})

test('speech during the restore ducks again at once', () => {
  const gain = fakeGain()
  const ducker = createDucker(gain)
  const t = feed(ducker, [speech, ...new Array<Float32Array>(25).fill(quiet)])
  gain.calls.length = 0
  ducker.hear(speech, RATE, t + 0.04)
  assert.deepEqual(gain.calls.map((c) => c[0]), ['cancel', 'target'])
})

test('reset brings Voice back to full at once', () => {
  const gain = fakeGain()
  const ducker = createDucker(gain)
  feed(ducker, [speech])
  gain.calls.length = 0
  ducker.reset(5)
  assert.deepEqual(gain.calls, [['cancel', 5], ['set', 1, 5]])
})
