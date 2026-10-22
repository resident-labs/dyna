import { test, beforeEach, afterEach, after } from 'node:test'
import { JSDOM } from 'jsdom'
import assert from 'node:assert/strict'
import { lastRoom, saveLastRoom, showReasoning, saveShowReasoning, channelListOf } from './prefs.ts'

const dom = new JSDOM('', { url: 'https://preferences.example.test' })
const storage = dom.window.localStorage
beforeEach(() => { storage.clear(); Object.assign(globalThis, { localStorage: storage }) })
afterEach(() => { Reflect.deleteProperty(globalThis, 'localStorage') })
after(() => dom.window.close())

test('unknown views reopen as general and nested channels persist', () => {
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

test('the sidebar channel list ignores the old agent channels switch', () => {
  assert.equal(channelListOf({}), 'all')
  assert.equal(channelListOf({ agentChannels: false }), 'all')
  assert.equal(channelListOf({ agentChannels: false, channelList: 'messages' }), 'messages')
  assert.equal(channelListOf({ channelList: 'bogus' }), 'all')
})
