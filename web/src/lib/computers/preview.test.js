import test from 'node:test'
import assert from 'node:assert/strict'
import { freshDue, previewVisible, inUse, snapCorner, loadPreview, savePreview, streamWanted, badgeText, DEFAULT_PREVIEW } from './preview.js'

const running = { state: 'running', shotAt: '2026-10-17T18:00:00Z' }
const base = { c: running, tab: 'chat', view: 'general', width: 1400, hidden: null }

test('the preview shows for running, starting and stopped with a picture', () => {
  assert.equal(previewVisible(base), true)
  assert.equal(previewVisible({ ...base, c: { state: 'starting' } }), true)
  assert.equal(previewVisible({ ...base, c: { state: 'stopped', shotAt: 'x' } }), true)
  assert.equal(previewVisible({ ...base, c: { state: 'stopped' } }), false)
  assert.equal(previewVisible({ ...base, c: { state: 'none' } }), false)
  assert.equal(previewVisible({ ...base, c: { state: 'idle' } }), false)
  assert.equal(previewVisible({ ...base, c: null }), false)
})

test('the preview hides on the Computer tab, other tabs, the computers page and a phone', () => {
  assert.equal(previewVisible({ ...base, tab: 'computer' }), false)
  assert.equal(previewVisible({ ...base, tab: 'media' }), false)
  assert.equal(previewVisible({ ...base, view: 'computers' }), false)
  assert.equal(previewVisible({ ...base, width: 699 }), false)
  assert.equal(previewVisible({ ...base, width: 700 }), true)
})

test('hide lasts until the computer changes state', () => {
  const hidden = { state: 'running' }
  assert.equal(previewVisible({ ...base, hidden }), false)
  assert.equal(previewVisible({ ...base, hidden, c: { state: 'stopped', shotAt: 'x' } }), true)
})

test('the header icon shows while the computer is in use', () => {
  assert.equal(inUse(running), true)
  assert.equal(inUse({ state: 'starting' }), true)
  assert.equal(inUse({ state: 'stopped', shotAt: 'x' }), false)
  assert.equal(inUse(null), false)
})

test('corners', () => {
  assert.equal(snapCorner(10, 10, 1000, 600), 'tl')
  assert.equal(snapCorner(900, 20, 1000, 600), 'tr')
  assert.equal(snapCorner(100, 500, 1000, 600), 'bl')
  assert.equal(snapCorner(900, 500, 1000, 600), 'br')
  assert.equal(snapCorner(500, 300, 1000, 600), 'br', 'the exact middle goes bottom right')
})

function fakeStore(init = {}) {
  const m = { ...init }
  return { getItem: (k) => m[k] ?? null, setItem: (k, v) => { m[k] = v }, m }
}

test('saved choices round trip per session and bad data gives defaults', () => {
  const s = fakeStore()
  assert.deepEqual(loadPreview('a', s), DEFAULT_PREVIEW)
  savePreview('a', { corner: 'tl', minimized: true, hidden: { state: 'running' } }, s)
  assert.deepEqual(loadPreview('a', s), { corner: 'tl', minimized: true, hidden: { state: 'running' } })
  assert.deepEqual(loadPreview('b', s), DEFAULT_PREVIEW)
  const bad = fakeStore({ 'dyna.computer.preview.x': '{"corner":"zz","minimized":"yes","hidden":5}', 'dyna.computer.preview.y': 'not json' })
  assert.deepEqual(loadPreview('x', bad), DEFAULT_PREVIEW)
  assert.deepEqual(loadPreview('y', bad), DEFAULT_PREVIEW)
  assert.deepEqual(loadPreview('a', null), DEFAULT_PREVIEW)
})

test('the stream follows the tab, the computer and the page', () => {
  const on = { tab: 'computer', c: running, pageVisible: true }
  assert.equal(streamWanted(on), true)
  assert.equal(streamWanted({ ...on, tab: 'chat' }), false, 'leaving the tab closes it')
  assert.equal(streamWanted({ ...on, pageVisible: false }), false, 'a hidden page closes it')
  assert.equal(streamWanted({ ...on, c: { state: 'idle' } }), false)
  assert.equal(streamWanted({ ...on, c: { state: 'starting' } }), false)
  assert.equal(streamWanted({ ...on, c: { state: 'stopped' } }), false)
  assert.equal(streamWanted({ ...on, pageVisible: undefined }), true)
})

test('badge text', () => {
  assert.equal(badgeText('connecting'), 'Connecting…')
  assert.equal(badgeText('connected', true), 'Live')
  assert.equal(badgeText('connected', false), '')
  assert.equal(badgeText('failed'), 'Could not connect')
  assert.equal(badgeText('ended'), 'Stream ended')
})

test('fresh pictures: only running, only visible, at most every interval', () => {
  const base = { phase: 'running', visible: true, last: 0, now: 5000, every: 5000 }
  assert.equal(freshDue(base), true)
  assert.equal(freshDue({ ...base, now: 4999 }), false)
  assert.equal(freshDue({ ...base, visible: false }), false)
  assert.equal(freshDue({ ...base, phase: 'stopped' }), false)
  assert.equal(freshDue({ ...base, phase: 'starting' }), false)
  assert.equal(freshDue({ ...base, last: 1000, now: 5500 }), false)
  assert.equal(freshDue({ ...base, last: 1000, now: 6000 }), true)
})

