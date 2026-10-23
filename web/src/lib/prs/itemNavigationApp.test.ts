import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { callTarget } from '../voice/callRequests.ts'
import { parsePRHash, parseIssueHash, parseFileHash, prHash, issueHash } from './prlink.ts'
import { ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute } from '../ui/route.ts'
import { call, moduleFunctions, sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { installGoals, installIssues, installMachines, installPins, installPRs, installQuestions, installSessionView, installSync } from '../../../test/appModules.ts'

const A = 'room_20261017-100000-aaaaaaaa'
const B = 'room_20261017-100000-bbbbbbbb'
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
// The screen's own navigation lives in lib/app/router.svelte.ts now.
const routerSource = readFileSync(new URL('../app/router.svelte.ts', import.meta.url), 'utf8')
const names = ['openReview', 'clearItemLink', 'holdsItemLink', 'syncRoute', 'routeHere', 'openView', 'selectRoom', 'openSession', 'openTarget', 'hashTarget', 'openPR']
const routerHandlers = moduleFunctions(routerSource, names)
const appHandlers = (parse(source, { modern: true }).instance?.content.body ?? []).filter((n) => n.type === 'FunctionDeclaration' && names.includes(n.id.name) && !routerHandlers.has(n.id.name)).map((n) => { const { start, end } = sourceRange(n); return stripTypes(source.slice(start, end)) })
const handlers = [...appHandlers, ...routerHandlers.values()].join('\n')
// The slice of App's state the handlers read and write.
interface Harness {
  location: { hash: string; pathname: string; search: string }
  entries: [string, string][]
  view: string
  prLink?: unknown
  roomId: string
  [name: string]: unknown
}
function app(hash: string): Harness {
  const location = { hash, pathname: '/', search: '' }
  const entries: [string, string][] = []
  const write = (kind: string, url: string) => { location.hash = url.includes('#') ? url.slice(url.indexOf('#')) : ''; entries.push([kind, location.hash]) }
  const history = { replaceState(_state: unknown, _unused: unknown, url: string) { write('replace', url) }, pushState(_state: unknown, _unused: unknown, url: string) { write('push', url) } }
  const c: Harness = {
    callTarget, callPush: null, URLSearchParams, location, history, entries, parsePRHash, parseIssueHash, parseFileHash, prHash, parseDetailHref: () => null,
    ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute, started: true, routing: 0, routeFix: false, pop: null, readOnlyDm: false, openRun: '',
    roomId: A, reviewOpen: false, reviewTab: 'files', reviewVersion: 0, isFeedView: (v: string) => v === 'general', loadChanges() {}, loadReviewer() {}, view: 'general', drill: null, detailRequest: 0, roomRequest: 0, sessionVisit: 0,
    GENERAL: 'general', hasTerminal: true, feed: null, feedAtBottom: false, needPair: false,
    selectionRevision: 0, consolidationRequest: 0, consolidationView: 0, questionsRequest: 0,
    rooms: [{ id: A }, { id: B }], ROOM: /^room_/, POST: /^msg_/,
    feedViews: { resolveView: (v: string) => v }, trimFeed: (v: unknown) => v, setPicked() {}, visitRoomControls() {},
    roomData: { newVisit() {}, open: async () => true, clearMembers() {}, clearFeed() {} }, narrow: false, sessionTurn: 0,
    loadSession() {}, setTimeout() {}, loadRuns() {}, jumpTo() {},
  }
  vm.createContext(c)
  vm.runInContext(handlers, c)
  // The machine list and the room's goals live in lib/app modules now.
  installMachines(c)
  installGoals(c)
  installQuestions(c)
  installPins(c)
  installIssues(c)
  installSync(c)
  installSessionView(c)
  installPRs(c)
  // The screen state lives in the router now, and the router asks the page for
  // the side of a screen change it does not own.
  installRouterShim(c)
  Reflect.set(c, 'host', {
    started: () => c.started,
    pop: () => !!c.pop,
    followHash: () => (c.hashTarget as () => boolean)(),
    leaveRoom: () => { void (c.selectRoom as (id: string) => Promise<boolean>)('') },
    closePanels: () => {},
    closeDrawer: () => {},
    resolveView: (v: string) => (c.feedViews as { resolveView: (view: string) => string }).resolveView(v),
    trimFeedAtView: () => {},
    leaveScreen: () => {},
    clearRunDetail: () => {},
    loadChanges: () => {},
    loadReviewer: () => {},
    loadRuns: () => {},
    loadRun: () => {},
  })
  return c
}

const session = 'ses_20261017-100000-cccccccc'
for (const hash of [prHash(A, 844, 'checks'), issueHash(A, 444)]) {
  test(`leaving ${hash} gives the new screen its own entry, so Back returns to the item`, async () => {
    const cases: [(c: Harness) => unknown, string][] = [
      [(c) => call(c, 'openView', 'goals'), `#room=${A}&view=goals`],
      [(c) => call(c, 'selectRoom', ''), ''],
      [(c) => call(c, 'selectRoom', B), `#room=${B}`],
      [(c) => call(c, 'openSession', { id: session }), `#room=${A}&session=${session}`],
    ]
    for (const [navigate, want] of cases) {
      const c = app(hash)
      c.view = hash.includes('issue=') ? 'issues' : 'github'
      c.prLink = { roomId: A }
      await navigate(c)
      call(c, 'syncRoute')
      assert.equal(c.prLink, null)
      assert.deepEqual(c.entries, [['push', want]])
    }
  })
}

test('switching between PRs and Issues drops the previous item link', () => {
  const c = app(prHash(A, 844))
  c.prLink = { roomId: A, pr: '844' }
  call(c, 'openView', 'issues')
  call(c, 'syncRoute')
  assert.equal(c.prLink, null)
  assert.deepEqual(c.entries, [['push', `#room=${A}&view=issues`]])
})

test('opening a PR keeps its new address', () => {
  const c = app('')
  call(c, 'openPR', 844)
  call(c, 'syncRoute')
  assert.equal(c.location.hash, prHash(A, 844))
  assert.deepEqual(c.entries, [['push', prHash(A, 844)]])
})

test('a reload or history link keeps the item address while its room loads', async () => {
  for (const hash of [prHash(B, 844, 'checks'), issueHash(B, 444)]) {
    const c = app(hash)
    assert.equal(call(c, 'hashTarget'), true)
    await new Promise(setImmediate)
    call(c, 'syncRoute')
    assert.equal(c.location.hash, hash)
    assert.deepEqual(c.entries, [])
    assert.equal(c.roomId, B)
    assert.equal(c.view, hash.includes('issue=') ? 'issues' : 'general')
  }
})
