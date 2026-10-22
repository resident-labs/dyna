import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { TAKEN_OVER, audioMsg, cancelMsg, fromWire, hangupMsg, muteMsg, playbackMsg } from './wire.js'

// The page and the engine share this file; the engine's Go test
// (internal/voice/realtime/protocol_test.go) holds its side to it.
const contract = JSON.parse(readFileSync(new URL('../../../../internal/voice/realtime/testdata/page-protocol.json', import.meta.url)))

test('the page sends the messages the engine reads', () => {
  const sent = [audioMsg('AQI='), muteMsg(true), cancelMsg(), playbackMsg('i1', 'started'), playbackMsg('i1', 'ended', 40), hangupMsg()]
  // A played_ms of 0 is the same as none to the engine.
  const onWire = sent.map(({ played_ms, ...rest }) => (played_ms ? { ...rest, played_ms } : rest))
  assert.deepEqual(onWire, contract.pageToEngine)
})

test('the page understands every message the engine sends', () => {
  for (const msg of contract.engineToPage) assert.notEqual(fromWire(msg), null, JSON.stringify(msg))
})

test('the page and the engine agree on the takeover close code', () => {
  assert.equal(TAKEN_OVER, contract.closeTakenOver)
})
