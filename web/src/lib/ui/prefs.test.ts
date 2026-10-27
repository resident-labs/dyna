import { test, beforeEach, afterEach, after } from 'node:test'
import { JSDOM } from 'jsdom'
import assert from 'node:assert/strict'
import { lastRoom, saveLastRoom, showReasoning, saveShowReasoning, feedViewListOf, loadDock, saveDock } from './prefs.ts'

const dom = new JSDOM('', { url: 'https://preferences.example.test' })
const storage = dom.window.localStorage
beforeEach(() => { storage.clear(); Object.assign(globalThis, { localStorage: storage }) })
afterEach(() => { Reflect.deleteProperty(globalThis, 'localStorage') })
after(() => dom.window.close())

test('unknown views reopen as general and nested views persist', () => {
  // Rules and Room settings moved from views into the Room settings modal.
  for (const view of ['all', 'owner', 'turns', 'settings', 'rules', 'room-general', 'room-hooks']) {
    storage.setItem('dyna.layout', JSON.stringify({ room: 'repo', view }))
    assert.deepEqual(lastRoom(), { room: 'repo', view: 'general' })
  }
  saveLastRoom('repo', 'channel:session-id')
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'channel:session-id' })
  saveLastRoom('repo', 'files')
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'files' })
  // Unknown saved views reopen the default feed.
  storage.setItem('dyna.layout', JSON.stringify({ room: 'repo', view: 'changes' }))
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'general' })
  saveLastRoom('repo', 'changes')
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'general' })
  saveLastRoom('repo', 'rules')
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'general' })
  saveLastRoom('repo', 'issues')
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'issues' })
})


test('Show reasoning defaults on and saves without replacing other display preferences', () => {
  let current: boolean | undefined
  const stop = showReasoning.subscribe((value) => { current = value })
  storage.setItem('dyna.layout', JSON.stringify({ side: false, room: 'alpha' }))
  try {
    assert.equal(current, true)
    saveShowReasoning(false)
    assert.equal(current, false)
    assert.deepEqual(JSON.parse(storage.getItem('dyna.layout') ?? '{}'), { side: false, room: 'alpha', showReasoning: false })
    saveShowReasoning(true)
    assert.equal(current, true)
    assert.equal(JSON.parse(storage.getItem('dyna.layout') ?? '{}').showReasoning, true)
  } finally {
    stop()
  }
})

test('the sidebar feed view list ignores the old agent views switch', () => {
  assert.equal(feedViewListOf({}), 'all')
  assert.equal(feedViewListOf({ agentChannels: false }), 'all')
  // The layout keeps its old key, so a saved list survives the rename.
  assert.equal(feedViewListOf({ agentChannels: false, channelList: 'messages' }), 'messages')
  assert.equal(feedViewListOf({ channelList: 'bogus' }), 'all')
})

test('the dock state is saved per room and does not erase other layout keys', () => {
  saveLastRoom('repo', 'files')
  saveDock('a', { open: true, height: 300, maxed: false, active: { a: 't1' } })
  saveDock('b', { open: false, height: 150, maxed: false, active: {} })
  assert.deepEqual(loadDock('a'), { open: true, height: 300, maxed: false, active: { a: 't1' } })
  assert.equal(loadDock('b').height, 150)
  assert.equal(loadDock('c').open, false)
  assert.deepEqual(lastRoom(), { room: 'repo', view: 'files' })
})
