import { test } from 'node:test'
import assert from 'node:assert/strict'
import { decodeAudio, downsample, encodeFrame, floatToPCM16 } from './pcm.ts'

test('downsample averages spans and passes lower rates through', () => {
  assert.deepEqual([...downsample(new Float32Array([0, 1, 0.5, 0.5, 1, 1]), 48000, 24000)], [0.5, 0.5, 1])
  const same = new Float32Array([0.1])
  assert.equal(downsample(same, 16000, 24000), same)
})

test('PCM16 round-trips through base64', () => {
  assert.deepEqual([...floatToPCM16(new Float32Array([0, 1, -1, 2]))], [0, 32767, -32768, 32767])
  const back = decodeAudio(encodeFrame(new Float32Array([0, 0.5, -0.5]), 24000, 24000))
  assert.equal(back.length, 3)
  assert.ok(Math.abs((back.at(1) ?? NaN) - 0.5) < 1e-3 && Math.abs((back.at(2) ?? NaN) + 0.5) < 1e-3)
})
