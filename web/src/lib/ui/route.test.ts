import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseRoute, roomHash, routeHash, routeSession, writeRoute, type Route } from './route.ts'
import { detailHref } from './details.ts'

const room = 'room_20261017-100000-aaaaaaaa'
const session = 'ses_20261017-100000-bbbbbbbb'
const run = 'wf_20261017-100000-cccccccc'
const at = (r: Partial<Route>): Route => ({ room, view: 'general', session: '', turn: 0, run: '', ...r })

test('every screen address round-trips', () => {
  for (const r of [at({}), at({ view: 'files' }), at({ view: `channel:${session}` }), at({ view: 'voice-call' }), at({ session }), at({ session, turn: 4 }), at({ session: 'root' }), at({ view: 'runs', run })]) {
    assert.deepEqual(parseRoute(routeHash(r)), r, routeHash(r))
  }
  assert.equal(routeHash(at({})), `#room=${room}`)
  assert.equal(roomHash(room, 'github'), `#room=${room}&view=github`)
  assert.equal(routeHash(at({ room: '' })), '', 'the all-rooms page has no address')
  assert.equal(routeHash(at({ view: 'Bad View' })), `#room=${room}`, 'a view parseRoute cannot read is not written')
})

test('completion links name the same session and run screens', () => {
  assert.deepEqual(parseRoute(detailHref({ kind: 'session', id: session, turn: 2 }, room)), at({ session, turn: 2 }))
  assert.deepEqual(parseRoute(detailHref({ kind: 'run', id: run }, room)), at({ view: 'runs', run }))
})

test('malformed parts fall back to the room, and a bad room names nothing', () => {
  assert.equal(parseRoute(''), null)
  assert.equal(parseRoute('#pair=ABC'), null)
  assert.equal(parseRoute('#room=../x&view=files'), null)
  for (const hash of [`#room=${room}&session=make+test`, `#room=${room}&run=../x`, `#room=${room}&view=<b>`, `#room=${room}&view=channel:a%20b`]) {
    assert.deepEqual(parseRoute(hash), at({}), hash)
  }
  assert.deepEqual(parseRoute(`#room=${room}&session=${session}&turn=-3`), at({ session }))
})

test('the root conversation and a session have their own route names', () => {
  assert.equal(routeSession(null), '')
  assert.equal(routeSession({ id: `root:${room}`, rootSeat: true }), 'root')
  assert.equal(routeSession({ id: session }), session)
})

test('writeRoute adds an entry for a new screen, corrects in place on replace, and skips the same address', () => {
  const loc = { hash: '', pathname: '/app', search: '?x=1' }
  const calls: string[][] = []
  const hist = {
    pushState(_s: unknown, _t: string, url?: string | URL | null) { calls.push(['push', String(url)]); loc.hash = String(url).slice(String(url).indexOf('#')) },
    replaceState(_s: unknown, _t: string, url?: string | URL | null) { calls.push(['replace', String(url)]); loc.hash = String(url).includes('#') ? String(url).slice(String(url).indexOf('#')) : '' },
  }
  writeRoute('', false, loc, hist)
  writeRoute(`#room=${room}`, true, loc, hist)
  writeRoute(`#room=${room}`, false, loc, hist)
  writeRoute(`#room=${room}&view=files`, false, loc, hist)
  writeRoute('', false, loc, hist)
  assert.deepEqual(calls, [['replace', `/app?x=1#room=${room}`], ['push', `/app?x=1#room=${room}&view=files`], ['push', '/app?x=1']])
})
