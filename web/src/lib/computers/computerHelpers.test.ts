import test from 'node:test'
import assert from 'node:assert/strict'
import { stateWord, hintText, escTwice, isLive, hasComputer, stateText, ageText, uptimeText, mib, appName, openedApps, streamLine, sizeText, shotRatio, pushAction, applyEvent, sortComputers, viewKey, popOut, FEED_KEEP, splitComputers, type ActionFeed, type Computer } from './computers.ts'

const now = Date.parse('2026-10-17T18:00:00Z')
const ago = (s: number): string => new Date(now - s * 1000).toISOString()

test('states', () => {
  assert.equal(isLive({ state: 'running' }), true)
  assert.equal(isLive({ state: 'idle' }), false)
  assert.equal(hasComputer({ state: 'none' }), false)
  assert.equal(hasComputer({ state: 'stopped' }), true)
  assert.equal(stateText({ state: 'none' }), 'No desktop yet')
  assert.equal(stateText({ state: 'stopped', reason: 'idle for 10 min' }), 'Stopped · idle for 10 min')
  assert.equal(stateText(undefined), 'No desktop yet')
  assert.equal(stateText({ state: 'starting' }), 'Starting…')
  assert.equal(hasComputer({ state: 'starting' }), true)
  assert.equal(isLive({ state: 'starting' }), false, 'the viewer waits until labwc runs')
})

test('age text', () => {
  assert.equal(ageText(ago(4), now), '4 s ago')
  assert.equal(ageText(ago(0), now), '0 s ago')
  assert.equal(ageText(ago(-5), now), '0 s ago', 'a clock a little ahead never shows a negative age')
  assert.equal(ageText(ago(125), now), '2 min ago')
  assert.equal(ageText(ago(7200), now), '2 h ago')
  assert.equal(ageText(undefined, now), 'no picture yet')
})

test('uptime text', () => {
  assert.equal(uptimeText(ago(30), now), 'up under 1 min')
  assert.equal(uptimeText(ago(12 * 60), now), 'up 12 min')
  assert.equal(uptimeText(ago(64 * 60), now), 'up 1 h 4 min')
  assert.equal(uptimeText(ago(3 * 3600), now), 'up 3 h')
  assert.equal(uptimeText(ago(-5), now), 'up under 1 min')
  assert.equal(uptimeText(undefined, now), '')
})

test('mib', () => {
  assert.equal(mib(0), '0 MiB')
  assert.equal(mib(undefined), '0 MiB')
  assert.equal(mib(24 * 1024), '24 MiB')
  assert.equal(mib(1536), '1.5 MiB')
  assert.equal(mib(230 * 1024 + 300), '230 MiB')
})

test('app name', () => {
  assert.equal(appName('org.mozilla.firefox'), 'Firefox')
  assert.equal(appName('google-chrome'), 'Google chrome')
  assert.equal(appName(''), '')
})

test('stream line', () => {
  assert.equal(streamLine({ stream: { running: false } }), 'off · 0 MiB')
  assert.equal(streamLine({}), 'off · 0 MiB')
  assert.equal(streamLine({ stream: { running: true, codec: 'H.264', encoder: 'VA-API' }, streamRssKiB: 24 * 1024 }), 'H.264 · VA-API · 24 MiB')
  assert.equal(streamLine({ stream: { running: true }, streamRssKiB: 0 }), 'on · 0 MiB')
  assert.equal(sizeText({ width: 1280, height: 800 }), '1280×800')
  assert.equal(sizeText({}), '')
  assert.equal(shotRatio({ width: 1280, height: 800 }), '1280 / 800')
  assert.equal(shotRatio({ width: 1000, height: 1000 }), '1000 / 1000')
  assert.equal(shotRatio({}), '16 / 10')
})

test('the action feed keeps the newest few', () => {
  let feed: ActionFeed = {}
  for (let i = 0; i < 10; i++) feed = pushAction(feed, 's', `click ${i}`, 1000 + i)
  assert.equal(feed.s?.length, FEED_KEEP)
  assert.deepEqual(feed.s?.at(-1), { line: 'click 9', at: 1009 })
  const same = pushAction(feed, 's', '')
  assert.equal(same, feed, 'an empty line changes nothing')
  assert.equal(pushAction({}, 'a', 'x').a?.[0]?.line, 'x')
})

test('state words and hints', () => {
  assert.equal(stateWord('connected', false), 'Live')
  assert.equal(stateWord('connected', true), 'You are driving')
  assert.equal(stateWord('connecting', false), 'Connecting')
  assert.equal(stateWord('failed', false), 'No stream')
  assert.match(hintText(false), /take control/)
  assert.match(hintText(true), /Esc twice/)
})

test('Esc twice within 600 ms', () => {
  const esc = escTwice()
  assert.equal(esc(1000), false)
  assert.equal(esc(1500), true)
  assert.equal(esc(1600), false, 'the pair is used up')
  assert.equal(esc(2300), false)
  assert.equal(esc(2301), true)
  assert.equal(esc(5000), false)
  assert.equal(esc(5700), false, 'too slow')
})

test('events patch the list', () => {
  const list: Computer[] = [{ session: 'a', name: 'alpha', state: 'idle' }, { session: 'b', name: 'beta', state: 'none' }]
  let r = applyEvent(list, { session: 'a', type: 'started', computer: { session: 'a', state: 'running', name: '' } })
  assert.equal(r.list[0]?.state, 'running')
  assert.equal(r.list[0]?.name, 'alpha', 'the room list keeps the name')
  assert.equal(list[0]?.state, 'idle', 'the old list is not changed')
  assert.equal(r.needsReload, false)
  r = applyEvent(list, { session: 'zzz', type: 'started', computer: { session: 'zzz', state: 'running' } })
  assert.equal(r.list, list)
  assert.equal(applyEvent(list, { session: 'a', type: 'stopped' }).needsReload, true)
  assert.equal(applyEvent(list, { session: 'a', type: 'act', action: 'click' }).needsReload, false)
})

test('sort', () => {
  const s = sortComputers([{ name: 'z', state: 'none' }, { name: 'b', state: 'stopped' }, { name: 'a', state: 'running' }, { name: 'c', state: 'running' }])
  assert.deepEqual(s.map((c) => c.name), ['a', 'c', 'b', 'z'])
})

test('view keys are the length and alphabet the server accepts', () => {
  const seen = new Set<string>()
  for (let i = 0; i < 50; i++) {
    const k = viewKey()
    assert.match(k, /^[A-Za-z0-9_-]{8,64}$/)
    seen.add(k)
  }
  assert.equal(seen.size, 50)
})

test('openedApps names an app that is new in the list', () => {
  const a = (id: string): { appId: string } => ({ appId: id })
  assert.deepEqual(openedApps([], [a('org.chromium.Chromium')]), ['Chromium'])
  assert.deepEqual(openedApps([a('foot')], [a('foot'), a('firefox')]), ['Firefox'])
  assert.deepEqual(openedApps([a('foot')], [a('foot')]), [])
  assert.deepEqual(openedApps(undefined, [a('foot')]), [])
})

test('popOut opens a computer in its own window, including root', () => {
  const opened: unknown[][] = [], events: { type?: string; detail?: unknown }[] = []
  const saved = { window: globalThis.window, location: globalThis.location, CustomEvent: globalThis.CustomEvent }
  Object.assign(globalThis, {
    location: { pathname: '/app' },
    CustomEvent: class {
      type: string
      detail: unknown
      constructor(type: string, init?: { detail?: unknown }) { this.type = type; this.detail = init?.detail }
    },
    window: { open: (...a: unknown[]) => (opened.push(a), {}), dispatchEvent: (e: { type?: string; detail?: unknown }) => events.push(e) },
  })
  try {
    popOut('room_20261018-000000-abcdef12', 'root-room_20261018-000000-abcdef12')
  } finally {
    Object.assign(globalThis, saved)
  }
  assert.deepEqual(opened[0]?.slice(0, 2), ['/app#pop=room_20261018-000000-abcdef12:root-room_20261018-000000-abcdef12', 'dyna-computer-root-room_20261018-000000-abcdef12'])
  assert.equal(events[0]?.type, 'dyna:computer-popped')
})

test('live computers are tiles; the rest are one-line rows', () => {
  const list: Computer[] = [
    { session: 'a', name: 'a', state: 'running' },
    { session: 'b', name: 'b', state: 'idle' },
    { session: 'c', name: 'c', state: 'stopped' },
    { session: 'd', name: 'd', state: 'none' },
    { session: 'e', name: 'e', state: 'starting' },
  ]
  const { tiles, rows } = splitComputers(list)
  assert.deepEqual(tiles.map((c) => c.name), ['a', 'e'])
  assert.deepEqual(rows.map((c) => c.name), ['b', 'c', 'd'])
})
