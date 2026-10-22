import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EARLY_MAX_SECONDS, createEarlySpeech } from './earlySpeech.ts'

const frame = (n: number, v: number): Float32Array => new Float32Array(n).fill(v)

test('take drops the silence before the first speech but keeps 100 ms of pre-roll', () => {
  const early = createEarlySpeech()
  early.push(frame(1000, 0), 1000) // one second of silence
  early.push(frame(500, 0.25), 1000) // then speech
  const { samples, rate } = early.take()
  assert.equal(rate, 1000)
  assert.equal(samples.length, 600)
  assert.equal(samples[99], 0)
  assert.equal(samples[100], 0.25)
  assert.equal(early.take().samples.length, 0, 'take empties the buffer')
})

test('silence alone gives nothing to send', () => {
  const early = createEarlySpeech()
  early.push(frame(2000, 0.001), 1000)
  assert.equal(early.take().samples.length, 0)
})

test('the buffer keeps only the latest 30 seconds, as the ChatGPT app does', () => {
  const early = createEarlySpeech()
  for (let i = 0; i < EARLY_MAX_SECONDS + 5; i++) early.push(frame(1000, i === 0 ? 0.5 : 0.25), 1000)
  const { samples } = early.take()
  assert.equal(samples.length, EARLY_MAX_SECONDS * 1000)
  assert.ok(!samples.includes(0.5), 'the oldest second was dropped')
})

test('a frame rate of zero or less throws, as the trim loop always did', () => {
  for (const rate of [0, -1]) {
    assert.throws(() => createEarlySpeech().push(frame(10, 0.25), rate), TypeError)
  }
})

test('clear drops everything', () => {
  const early = createEarlySpeech()
  early.push(frame(100, 0.25), 1000)
  early.clear()
  assert.equal(early.take().samples.length, 0)
})
