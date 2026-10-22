import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { setImmediate } from 'node:timers/promises'
import { parse } from 'svelte/compiler'
import { GENERAL, VOICE, isChannel, inChannel, homeChannel, channelModel } from './channels.js'
import { appLoaderStubs } from './appStubs.js'
import { reconcileRows } from '../ui/reloadGate.js'
import { createRoomData } from './roomData.js'
import { pruneSelection } from '../sessions/consolidation.ts'
import { detailHref, parseDetailHref } from '../ui/details.js'
import { callTarget } from '../voice/callRequests.ts'
import { prHash, parsePRHash, issueHash, parseIssueHash, parseFileHash } from '../prs/prlink.ts'
import { roomIDPattern as ROOM, postIDPattern as POST, postHref } from '../ui/posts.js'
import { questionID } from './questions.js'
import { isShellResult } from './feedRows.js'
import { trimFeed } from './feedWindow.js'
import { mergeFeedPage, rerouteAnchors, withChannels } from './feedPages.js'
import { reloadsUnread } from '../ui/unreadReload.ts'
import { ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute } from '../ui/route.ts'

// Run the actual App navigation functions with controlled API responses.
// The Svelte parser selects complete declarations without copying their logic.
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const declarations = parse(source).instance.content.body
const names = ['clearItemLink', 'holdsItemLink', 'syncRoute', 'routeHere', 'hashChanged', 'setPicked', 'selectRoom', 'openTarget', 'hashTarget', 'openDetail', 'openView', 'jumpTo', 'showRun', 'openPR']
const code = declarations.filter((node) => node.type === 'FunctionDeclaration' && names.includes(node.id.name)).map((node) => source.slice(node.start, node.end)).join('\n')
assert.equal(declarations.filter((n) => n.type === 'FunctionDeclaration' && names.includes(n.id.name)).length, names.length)

const A = 'room_20261017-100000-aaaaaaaa'
const B = 'room_20261017-100000-bbbbbbbb'
const target = (kind) => ({ kind, id: `${{ shell: 'sh', run: 'wf', session: 'ses' }[kind]}_20261017-100000-aaaaaaaa`, turn: kind === 'session' ? 3 : 0, roomId: A })
const opened = (id) => ({ feed: { room: { id }, posts: [] } })

function deferred() {
  let resolve
  const promise = new Promise((r) => { resolve = r })
  return { promise, resolve }
}

function navigation() {
  const pending = deferred()
  const calls = []
  const applied = []
  const c = {
    roomId: B, roomRequest: 0, questionsRequest: 0, detailRequest: 0, narrow: false, hasTerminal: true,
    selectionRevision: 0, consolidationView: 0, consolidationAlive: true, memberRequest: 0, pickedSessions: [], pruneSelection,
    showClosed: false, rooms: [{ id: A }, { id: B }], needPair: false,
    ROOM, POST, GENERAL, VOICE, routedParents: '', isChannel, inChannel, homeChannel, isShellResult, trimFeed, mergeFeedPage, questionID, channels: channelModel([]), posts: [],
    view: GENERAL, openRun: '', openJob: '', drill: null, feed: null, feedAtBottom: true, location: { hash: '', pathname: '/', search: '' },
    started: true, routing: 0, routeFix: false, pop: null, readOnlyDm: false, sessionTurn: 0, entries: [],
    ROOT_SESSION, parseRoute, routeHash, routeSession, writeRoute, parseDetailHref, prHash, parsePRHash, parseIssueHash, parseFileHash, callTarget, callPush: null, URLSearchParams, tick: () => Promise.resolve(),
    loadRooms() {}, visitRoomControls() {}, loadConsolidations() {}, markShown() {}, loadEarlier() {}, say() {},
    loadRuns() {}, loadRun() { calls.push('run') },
    feedView: { jump() { calls.push('post') } },
    openSession() { calls.push('session') },
    applyOpened(o) { c.feed = o.feed; applied.push(o.feed.room.id) },
    api: {
      openRoom(id) { return id === A ? pending.promise : Promise.resolve(opened(id)) },
      transcriptTurn() { calls.push('transcript'); return Promise.resolve({ member: {} }) },
      transcript() { calls.push('transcript'); return Promise.resolve({ member: {} }) },
    },
  }
  c.roomData = createRoomData(c.api, {
    current: () => ({ room: c.roomId, visit: c.roomRequest }),
    opened: (value, initial) => {
      if (initial) c.unreadFrom = (value.feed.posts ?? []).find((p) => p.unread)?.id ?? ''
      c.applyOpened(value)
    },
    error: (error) => (c.loadError = error),
  })
  // entries records each address write: ['push' | 'replace', hash].
  const write = (kind, url) => { c.location.hash = url.includes('#') ? url.slice(url.indexOf('#')) : ''; c.entries.push([kind, c.location.hash]) }
  c.history = { replaceState(_state, _title, url) { write('replace', url) }, pushState(_state, _title, url) { write('push', url) } }
  vm.createContext(c)
  vm.runInContext(code, c)
  return { c, pending, calls, applied }
}

for (const kind of ['shell', 'run', 'session']) {
  for (const later of ['room', 'view']) {
    test(`a delayed cross-room ${kind} link preserves a later ${later} choice`, async () => {
      const { c, pending, calls, applied } = navigation()
      const opening = c.openDetail(target(kind))
      if (later === 'room') await c.selectRoom(B)
      else c.openView('files')
      pending.resolve(opened(A))
      await opening
      assert.equal(c.roomId, later === 'room' ? B : A)
      assert.equal(c.view, later === 'room' ? GENERAL : 'files')
      assert.equal(c.openJob, '')
      assert.deepEqual(calls, [])
      assert.deepEqual(applied, [later === 'room' ? B : A])
    })
  }
}

for (const later of ['room', 'view']) {
  test(`a copied cross-room link preserves a later ${later} choice`, async () => {
    const { c, pending, calls } = navigation()
    c.location.hash = detailHref(target('session'), A)
    assert.equal(c.hashTarget(), true)
    if (later === 'room') await c.selectRoom(B)
    else c.openView('files')
    pending.resolve(opened(A))
    await setImmediate()
    assert.equal(c.roomId, later === 'room' ? B : A)
    assert.equal(c.view, later === 'room' ? GENERAL : 'files')
    assert.equal(c.openJob, '')
    assert.deepEqual(calls, [])
  })
}

test('the copied-hash continuation also stops after its room target has resolved', async () => {
  const { c, pending, calls } = navigation()
  const openTarget = c.openTarget
  c.openTarget = async (...args) => {
    const selected = await openTarget(...args)
    queueMicrotask(() => c.openView('files'))
    return selected
  }
  c.location.hash = detailHref(target('shell'), A)
  c.hashTarget()
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.view, 'files')
  assert.equal(c.openJob, '')
  assert.deepEqual(calls, [])
})

test('a PR link opens the Pull requests view on that PR and stays in the address bar', async () => {
  const { c, pending } = navigation()
  c.location.hash = prHash(A, 741, 'checks')
  assert.equal(c.hashTarget(), true)
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.equal(c.view, 'github')
  assert.deepEqual({ ...c.prLink }, { roomId: A, pr: '741', tab: 'checks' })
  assert.equal(c.location.hash, prHash(A, 741, 'checks'))
  // A later view choice drops the link, so the PR screen does not reopen it.
  c.openView('files')
  assert.equal(c.prLink, null)
})

test('Back and Forward between PR links stay with the open Pull requests view', () => {
  const { c } = navigation()
  c.view = 'github'
  let opened = false
  c.openTarget = () => { opened = true; return Promise.resolve(false) }
  c.location.hash = prHash(B, 742)
  assert.equal(c.hashTarget(), true)
  assert.equal(opened, false)
  assert.equal(c.location.hash, prHash(B, 742))
})

test('a current cross-room link still opens the requested log', async () => {
  const { c, pending } = navigation()
  const opening = c.openDetail(target('shell'))
  pending.resolve(opened(A))
  await opening
  assert.equal(c.roomId, A)
  assert.equal(c.view, 'jobs')
  assert.equal(c.openJob, target('shell').id)
})

test('a current copied link still opens its exact transcript', async () => {
  const { c, pending, calls } = navigation()
  c.location.hash = detailHref(target('session'), A)
  c.hashTarget()
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.deepEqual(calls, ['transcript', 'session'])
})

test('opening a room or channel does not load the whole history', async () => {
  const { c } = navigation()
  let loads = 0
  c.loadEarlier = () => { loads++ }
  c.channels = channelModel([{ id: 'lead', address: 'repo:lead' }])
  assert.equal(await c.selectRoom(B), true)
  c.openView('channel:lead')
  assert.equal(loads, 0)
})

test('a later same-room selection wins over an older room response', async () => {
  const { c, pending, applied } = navigation()
  const first = c.selectRoom(A)
  c.api.openRoom = () => Promise.resolve(opened(A))
  assert.equal(await c.selectRoom(A, 'files'), true)
  pending.resolve(opened(A))
  assert.equal(await first, false)
  assert.equal(c.view, 'files')
  assert.deepEqual(applied, [A])
})

for (const workspace of ['files', 'github', 'jobs']) {
  test(`opening saved ${workspace} does not mark hidden channel messages read`, async () => {
    const { c } = navigation()
    c.posts = [{ id: 'hidden-child-result', unread: true }, { id: 'other-channel-result', unread: true }]
    const marked = []
    c.markShown = (posts) => { marked.push(...posts) }
    assert.equal(await c.selectRoom(B, workspace), true)
    assert.equal(c.view, workspace)
    assert.deepEqual(marked, [])
  })
}

test('a pin 10,000 posts back waits for the in-flight page then uses one around request', async () => {
  const { c, calls } = navigation()
  const child = { id: 'child', address: 'repo:child', parent: 'lead' }
  c.channels = channelModel([{ id: 'lead', address: 'repo:lead' }, child])
  c.feed = { room: { id: B }, posts: [], earlier: 10000, cursor: 'first' }
  c.earlierLoad = null
  c.STEP = 100
  const history = deferred()
  const result = { id: 'old-result', from: child.address, completion: true, channels: ['channel:lead'] }
  const pages = []
  c.api.feedTail = (id, before, limit) => {
    pages.push([id, before, limit])
    return pages.length === 1 ? history.promise : Promise.resolve({ room: { id: B }, posts: [result], earlier: 0 })
  }
  c.api.feedAround = (id, target, limit) => {
    pages.push([id, target, limit])
    return Promise.resolve({ room: { id: B }, posts: [result], earlier: 1, newer: 9999 })
  }
  const declaration = declarations.find((node) => node.type === 'FunctionDeclaration' && node.id.name === 'loadEarlier')
  vm.runInContext(source.slice(declaration.start, declaration.end), c)
  const loading = c.loadEarlier()
  const jumping = c.jumpTo(result.id)
  await setImmediate()
  assert.deepEqual(calls, [])
  history.resolve({ room: { id: B }, posts: [], earlier: 100, cursor: 'second' })
  await Promise.all([loading, jumping])
  assert.equal(c.view, 'channel:lead')
  assert.deepEqual(calls, ['post'])
  assert.deepEqual(pages, [[B, 'first', 100], [B, result.id, 100]])
})

test('a pin to a shell result opens its job in shell-jobs', async () => {
  const { c, calls } = navigation()
  const shell = 'sh_20261017-120000-0000000a'
  c.feed = { room: { id: B }, posts: [{ id: 'shell-result', from: `shell:${shell}`, kind: 'failed', completion: true }] }
  await c.jumpTo('shell-result')
  assert.equal(c.view, 'jobs')
  assert.equal(c.openJob, shell)
  assert.deepEqual(calls, [])
})

test('a pin waiting for history preserves a later workspace selection', async () => {
  const { c, calls } = navigation()
  const history = deferred()
  c.feed = { room: { id: B }, posts: [], earlier: 1, cursor: 'first' }
  c.loadEarlier = async () => { await history.promise; return false }
  const jumping = c.jumpTo('old-result')
  c.openView('files')
  history.resolve()
  await jumping
  assert.equal(c.view, 'files')
  assert.deepEqual(calls, [])
})

test('reconnect moves a stale lead channel to general after its last child is promoted', async () => {
  const { c } = navigation()
  const lead = { id: 'lead', name: 'lead', address: 'repo:lead' }
  const child = { id: 'child', name: 'child', address: 'repo:child', parent: 'lead' }
  c.hierarchy = [lead, child]
  Object.defineProperty(c, 'channels', { get: () => channelModel(c.hierarchy) })
  c.view = 'channel:lead'
  c.feed = { room: { id: B }, posts: [] }
  c.openRun = ''
  c.reconcileRows = reconcileRows
  c.api.openRoom = async () => ({ ...opened(B), members: [lead, { ...child, parent: '' }] })
  for (const name of ['parentKey', 'noteParents', 'applyOpened', 'resync']) {
    const node = declarations.find((node) => node.type === 'FunctionDeclaration' && node.id.name === name)
    vm.runInContext(source.slice(node.start, node.end), c)
  }
  await c.resync()
  assert.deepEqual(c.channels.leads, [])
  assert.equal(c.view, GENERAL)
})

test('a reconnect after a promote rereads the channels of loaded completions in a history window', async () => {
  const { c } = navigation()
  const lead = { id: 'lead', name: 'lead', address: 'repo:lead' }
  const child = { id: 'child', name: 'child', address: 'repo:child', parent: 'lead' }
  const done = { id: 'done', from: child.address, completion: true, channels: ['channel:lead'] }
  Object.defineProperty(c, 'channels', { get: () => channelModel(c.hierarchy) })
  Object.assign(c, { view: 'channel:lead', openRun: '', reconcileRows, rerouteAnchors, withChannels, routedParents: '', feedAtBottom: false, workflowData: { loadJobs() {} }, ...appLoaderStubs(), roomSessionController: null, drill: null })
  for (const name of ['loadReviewer', 'loadChanges', 'loadGoals', 'loadRules', 'loadVisuals']) c[name] = () => {}
  for (const name of ['parentKey', 'noteParents', 'rerouteLoaded', 'applyOpened', 'resync']) {
    const node = declarations.find((node) => node.type === 'FunctionDeclaration' && node.id.name === name)
    vm.runInContext(source.slice(node.start, node.end), c)
  }
  c.roomId = B
  c.feed = { room: { id: B }, posts: [] }
  c.applyOpened({ ...opened(B), members: [lead, child] })
  // The owner reads history: newer posts are missing, and a completion is loaded.
  c.feed = { room: { id: B }, posts: [done], newer: 30 }
  // While the stream is down, the only child is promoted. The reconnect's tail
  // has neither the loaded post nor the old parent.
  c.api.openRoom = async () => ({ ...opened(B), members: [lead, { ...child, parent: '' }] })
  c.api.feedAround = async () => ({ room: { id: B }, posts: [{ ...done, channels: ['general'] }] })
  await c.resync()
  await setImmediate()
  assert.equal(c.view, GENERAL)
  assert.deepEqual(c.feed.posts.find((p) => p.id === 'done')?.channels, ['general'])
})

test('changing rooms resets multi-selection and closes its previous consolidation screen', async () => {
  const { c } = navigation()
  c.selectingSessions = true; c.pickedSessions = ['one', 'two']
  c.consolidating = { roomId: B, sessions: ['one', 'two'] }
  assert.equal(await c.selectRoom(''), true)
  assert.equal(c.selectingSessions, false)
  assert.equal(c.pickedSessions.length, 0)
  assert.equal(c.consolidating, null)
})

test('opening a source post closes its transcript and overlays before showing the room post', async () => {
  const { c, calls } = navigation()
  c.drill = { id: 'worker' }
  c.card = { id: 'worker' }
  c.viewing = { id: 'attachment', postId: 'msg_stored' }
  c.feed = { room: { id: B }, posts: [{ id: 'msg_stored', from: 'repo:worker', to: ['repo'] }] }
  await c.jumpTo('msg_stored')
  assert.equal(c.drill, null)
  assert.equal(c.card, null)
  assert.equal(c.viewing, null)
  assert.deepEqual(calls, ['post'])
})

test('a link to a voice call line keeps the current view', async () => {
  const { c, calls } = navigation()
  c.view = 'channel:lead'
  c.feed = { room: { id: B }, posts: [{ id: 'msg_call', from: 'repo:voice-call', to: ['repo'], channels: ['voice-call'] }] }
  await c.jumpTo('msg_call')
  assert.equal(c.view, 'channel:lead')
  assert.deepEqual(calls, [])
})

test('copied source-post links open the global room', async () => {
  const { c, calls } = navigation()
  c.rooms.push({ id: 'global' })
  c.location.hash = postHref('global', 'msg_stored')
  c.api.openRoom = (id) => Promise.resolve(opened(id))
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.equal(c.roomId, 'global')
  assert.deepEqual(calls, ['post'])
})

test('opening a view keeps only the newest posts of a long feed', () => {
  const { c } = navigation()
  c.feed = { room: { id: B }, earlier: 0, posts: Array.from({ length: 700 }, (_, i) => ({ id: `p${i}` })) }
  c.openView(GENERAL)
  assert.equal(c.feed.posts.length, 300)
  assert.equal(c.feed.posts[0].id, 'p400')
  assert.equal(c.feed.earlier, 400)
})

test('selecting the open view again keeps older posts the reader is looking at', () => {
  const { c } = navigation()
  c.feed = { room: { id: B }, earlier: 0, posts: Array.from({ length: 700 }, (_, i) => ({ id: `p${i}` })) }
  c.feedAtBottom = false
  c.openView(GENERAL)
  assert.equal(c.feed.posts.length, 700)
  // At the bottom, the same view trims as usual.
  c.feedAtBottom = true
  c.openView(GENERAL)
  assert.equal(c.feed.posts.length, 300)
})

test('a pin leaves the bottom before it loads older posts, so a live post cannot trim them', async () => {
  const { c } = navigation()
  let atBottom
  c.feed = { room: { id: B }, posts: [], earlier: 1, cursor: 'newer-result' }
  c.loadEarlier = async () => { atBottom = c.feedAtBottom; return false }
  await c.jumpTo('old-result')
  assert.equal(atBottom, false)
})

test('an older page cannot merge after a same-room refresh changes its boundary', async () => {
  const pending = deferred()
  const before = { room: { id: B }, posts: [{ id: 'p151' }, { id: 'p200' }], cursor: 'p151', earlier: 150 }
  const refreshed = { room: { id: B }, posts: [{ id: 'p181' }, { id: 'p250' }], cursor: 'p181', earlier: 180 }
  const c = { roomId: B, roomRequest: 1, earlierLoad: null, feed: before, feedAtBottom: true, STEP: 50, mergeFeedPage,
    api: { feedTail: (id, cursor) => { assert.equal(cursor, 'p151'); return pending.promise } } }
  vm.createContext(c)
  const fn = declarations.find((node) => node.type === 'FunctionDeclaration' && node.id.name === 'loadEarlier')
  vm.runInContext(source.slice(fn.start, fn.end), c)
  const loading = c.loadEarlier()
  c.feed = refreshed
  pending.resolve({ room: { id: B }, posts: [{ id: 'p51' }, { id: 'p150' }], cursor: 'p51', earlier: 50 })
  assert.equal(await loading, false)
  assert.equal(c.feed, refreshed)
  assert.equal(c.earlierLoad, null)
})


test('an issue link opens Issues and keeps its address through initial navigation', async () => {
  const { c, pending } = navigation()
  c.location.hash = issueHash(A, 839)
  assert.equal(c.hashTarget(), true)
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.equal(c.view, 'issues')
  assert.deepEqual({ ...c.prLink }, { roomId: A, issue: 839 })
  assert.equal(c.location.hash, issueHash(A, 839))
})

test('Back and Forward between issues stay with the open Issues view', () => {
  const { c } = navigation()
  c.view = 'issues'
  c.openTarget = () => { throw new Error('Issues follows its own link') }
  c.location.hash = issueHash(B, 839)
  assert.equal(c.hashTarget(), true)
  assert.equal(c.location.hash, issueHash(B, 839))
})


test('an issue timeline PR link records the PR address for reload and Back', () => {
  const { c } = navigation()
  c.view = 'issues'
  c.location.hash = issueHash(B, 839)
  c.openPR(841)
  assert.equal(c.view, 'github')
  assert.equal(c.location.hash, prHash(B, 841))
  assert.deepEqual({ ...c.prLink }, { roomId: B, pr: '841', tab: 'conversation' })
})

for (const itemHash of [prHash(B, 844), issueHash(B, 444)]) {
  test(`a pinned post or run gets a new entry after the item address ${itemHash}, so Back reopens the item`, async () => {
    const run = 'wf_20261017-120000-0000000b'
    for (const kind of ['post', 'shell', 'run']) {
      const { c } = navigation()
      c.location.hash = itemHash
      c.view = itemHash.includes('issue=') ? 'issues' : 'github'
      c.prLink = { roomId: B }
      c.feed = { room: { id: B }, posts: [{ id: 'pin', from: kind === 'shell' ? 'shell:sh_20261017-120000-0000000a' : 'owner', kind: 'failed', completion: true }] }
      if (kind === 'run') c.showRun({ id: run })
      else await c.jumpTo('pin')
      c.syncRoute()
      assert.equal(c.prLink, null)
      const view = kind === 'run' ? 'runs' : kind === 'shell' ? 'jobs' : GENERAL
      assert.equal(c.view, view)
      assert.deepEqual(c.entries, [['push', kind === 'run' ? `#room=${B}&run=${run}` : routeHash({ room: B, view, session: '', turn: 0, run: '' })]])
    }
  })
}

test('the address follows a session, and Back to the room address closes it without a room reload', async () => {
  const { c, calls, applied } = navigation()
  c.location.hash = `#room=${B}`
  const m = { id: 'ses_20261017-100000-cccccccc', name: 'lead' }
  c.drill = m
  c.syncRoute()
  assert.deepEqual(c.entries, [['push', `#room=${B}&session=${m.id}`]])
  // Back: the browser shows the room address again and fires hashchange.
  c.location.hash = `#room=${B}`
  assert.equal(c.hashTarget(), true)
  assert.equal(c.routing, 1, 'the address waits while the link is followed')
  await setImmediate()
  assert.equal(c.routing, 0)
  assert.equal(c.drill, null)
  assert.equal(c.view, GENERAL)
  assert.deepEqual(applied, [], 'the open room is not reloaded')
  assert.deepEqual(calls, [])
  c.syncRoute()
  assert.equal(c.entries.length, 1, 'the landed screen matches its address')
})

test('Back to the room while a session address still loads keeps the room the reader went back to', async () => {
  const { c, calls } = navigation()
  const id = 'ses_20261017-100000-cccccccc'
  const loading = deferred()
  c.api.transcript = () => { calls.push('transcript'); return loading.promise }
  c.location.hash = `#room=${B}&session=${id}`
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.deepEqual(calls, ['transcript'], 'the session address asks for its transcript')
  // Back before the transcript lands: the room address matches the open screen.
  c.location.hash = `#room=${B}`
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  loading.resolve({ member: { id, name: 'lead' } })
  await setImmediate()
  assert.ok(!calls.includes('session'), 'the late transcript does not open over the room')
  c.syncRoute()
  assert.equal(c.location.hash, `#room=${B}`, 'the room keeps the address Back restored')
  assert.ok(c.entries.every(([kind]) => kind === 'replace'), 'no session entry is pushed')
})

test('a reloaded session address reopens the session and keeps one history entry', async () => {
  const { c, pending, calls } = navigation()
  const id = 'ses_20261017-100000-cccccccc'
  c.location.hash = `#room=${A}&session=${id}&turn=2`
  assert.equal(c.hashTarget(), true)
  c.syncRoute()
  assert.deepEqual(c.entries, [], 'nothing is written while the room loads')
  pending.resolve(opened(A))
  await setImmediate()
  assert.equal(c.roomId, A)
  assert.deepEqual(calls, ['transcript', 'session'])
})

test('a followed address that cannot open is corrected in place', async () => {
  const { c } = navigation()
  c.location.hash = `#room=${B}&view=files`
  c.view = 'files'
  c.routeFix = false
  // The run address names a run; the screen lands on the room instead.
  c.showRun = () => {}
  c.location.hash = `#room=${B}&run=wf_20261017-100000-dddddddd`
  c.hashTarget()
  await setImmediate()
  c.syncRoute()
  assert.deepEqual(c.entries.at(-1), ['replace', `#room=${B}&view=files`])
})

test('an open PR keeps its own address; the route waits until the view leaves it', () => {
  const { c } = navigation()
  c.view = 'github'
  c.location.hash = prHash(B, 9)
  c.syncRoute()
  assert.deepEqual(c.entries, [])
  c.openView('files')
  c.syncRoute()
  assert.deepEqual(c.entries, [['push', `#room=${B}&view=files`]])
})

test('successful read batches refresh channel counts through the existing room debounce', () => {
  const callback = declarations.find((n) => n.type === 'VariableDeclaration' && n.declarations.some((d) => d.id.name === 'readMarker')).declarations[0].init.arguments[1].properties.find((p) => p.key.name === 'read').value
  const refresh = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'soonRooms')
  let timer, rooms = 0
  const tails = []
  const c = { roomRefreshes: new Set(), roomTimer: null, roomId: A, feed: { posts: [{ id: 'one' }] },
    setTimeout(fn) { timer = fn; return 1 }, clearTimeout() {},
    loadRooms() { rooms++ }, roomData: { loadUnread() { tails.push('unread') } },
  }
  vm.createContext(c)
  vm.runInContext(source.slice(refresh.start, refresh.end) + '\nvar afterRead = ' + source.slice(callback.start, callback.end), c)
  c.afterRead()
  c.afterRead()
  assert.equal(rooms, 0)
  assert.deepEqual(tails, [])
  timer()
  assert.equal(rooms, 1)
  assert.deepEqual(tails, ['unread'])
  c.roomId = ''
  c.afterRead()
  timer()
  assert.deepEqual(tails, ['unread'], 'leaving the room drops its pending feed refresh')
})


test('channel count refresh preserves over 200 displayed posts while scrolled up', async () => {
  const posts = Array.from({ length: 250 }, (_, i) => ({ id: `post-${i}` }))
  const c = { roomId: A, roomRequest: 1, feedAtBottom: false, roomTimer: null, roomRefreshes: new Set(),
    feed: { room: { id: A }, posts, offset: 900, earlier: 20, cursor: 'post-0', channelUnread: {} },
    applyItemEvent: () => false, reloadsUnread, home: null, drill: null, roomSessionController: null, loadMembers() {}, loadRooms() {},
    setTimeout(fn) { c.timer = fn }, clearTimeout() {},
  }
  vm.createContext(c)
  const refresh = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'soonRooms')
  const hook = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'applyUnread')
  const handle = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === 'handle')
  vm.runInContext(source.slice(refresh.start, refresh.end) + '\n' + source.slice(handle.start, handle.end) + '\n' + source.slice(hook.start, hook.end), c)
  const requests = []
  c.roomData = createRoomData({
    feedTail: async (...args) => { requests.push(args); return { room: { id: A }, posts: posts.slice(-1), channelUnread: { 'channel:lead': 3 } } },
    openRoom: async () => ({ feed: { room: { id: A }, posts: posts.slice(-200) } }),
  }, { current: () => ({ room: c.roomId, visit: c.roomRequest }), unread: c.applyUnread,
    opened(value) { c.feed = mergeFeedPage(c.feed, value.feed) }, error(e) { throw e },
  })
  c.handle({ kind: 'members', roomId: A })
  c.timer()
  await setImmediate()
  assert.equal(c.feed.posts, posts)
  assert.equal(c.feed.posts.length, 250)
  assert.equal(c.feed.cursor, 'post-0')
  assert.equal(c.feed.earlier, 20)
  assert.equal(c.feed.offset, 900)
  assert.equal(c.feed.channelUnread['channel:lead'], 3)
  assert.deepEqual(requests, [[A, '', 1]])
  c.roomData.destroy()
})

test('unread metadata from an earlier room visit cannot update the current sidebar', async () => {
  const wait = deferred()
  let current = { room: A, visit: 1 }, updates = 0
  const data = createRoomData({ feedTail: () => wait.promise }, {
    current: () => current, unread() { updates++ },
  })
  const loading = data.loadUnread()
  current = { room: A, visit: 2 }
  wait.resolve({ room: { id: A }, channelUnread: { 'channel:lead': 3 } })
  await loading
  assert.equal(updates, 0)
  data.destroy()
})

test('an older page cannot trim the post a reader moved to while it was loading', async () => {
 const {c}=navigation()
 const original={room:{id:B},posts:Array.from({length:600},(_,i)=>({id:'p'+i})),earlier:50,cursor:'p0'}
 const pending=deferred();let near=true
 Object.assign(c,{feed:original,earlierLoad:null,STEP:50})
 c.api.feedTail=()=>pending.promise
 const declaration=declarations.find(n=>n.type==='FunctionDeclaration'&&n.id.name==='loadEarlier')
 vm.runInContext(source.slice(declaration.start,declaration.end),c)
 const loading=c.loadEarlier('older','',()=>near)
 near=false
 pending.resolve({room:{id:B},posts:[{id:'older'}],earlier:0})
 await loading
 assert.equal(c.feed,original)
})

test('opening a goal that no loaded page holds loads it, and a done goal opens under Done', async () => {
  const { c } = navigation()
  const { goalFilter } = await import('./goals.js')
  const old = { id: 'g9', state: 'done', text: 'Old goal' }
  const calls = []
  c.goals = [{ id: 'g1', state: 'active' }]
  c.openView = () => {}
  c.api.goal = async (room, id) => { calls.push([room, id]); return old }
  c.fetchedGoal = null
  c.fetchedGoalVersion = 0
  for (const name of ['goalById', 'openGoal', 'trackFetchedGoal', 'refreshFetchedGoal']) {
    const node = declarations.find((n) => n.type === 'FunctionDeclaration' && n.id.name === name)
    vm.runInContext(source.slice(node.start, node.end), c)
  }
  assert.equal(await c.openGoal('g9'), old)
  assert.equal(c.goalId, 'g9')
  assert.deepEqual(calls, [[B, 'g9']])
  // An unrelated goals refresh rebuilds the list without the old goal.
  c.goals = [{ id: 'g1', state: 'active' }, { id: 'g2', state: 'active' }]
  assert.equal(c.goalById('g9'), old, 'the fetched goal stays selectable')
  // An edit to the old goal arrives as a change event, or after a reload.
  c.trackFetchedGoal({ op: 'upsert', id: 'g9', item: { ...old, text: 'Edited' } })
  assert.equal(c.goalById('g9').text, 'Edited')
  c.api.goal = async (room, id) => { calls.push([room, id]); return { ...old, text: 'Reloaded' } }
  await c.refreshFetchedGoal()
  assert.equal(c.goalById('g9').text, 'Reloaded')
  // A change event that lands while a refresh is in flight wins over its
  // older answer.
  let answer
  c.api.goal = () => new Promise((resolve) => (answer = resolve))
  const refreshing = c.refreshFetchedGoal()
  c.trackFetchedGoal({ op: 'upsert', id: 'g9', item: { ...old, text: 'From the event', holders: ['lead'] } })
  answer({ ...old, text: 'Older answer' })
  await refreshing
  assert.equal(c.goalById('g9').text, 'From the event')
  assert.deepEqual([...c.goalById('g9').holders], ['lead'])
  calls.length = 1
  c.trackFetchedGoal({ op: 'delete', id: 'g9' })
  assert.equal(c.goalById('g9'), undefined)
  assert.equal(goalFilter(old), 'done')
  assert.equal(goalFilter({ state: 'dropped' }), 'done')
  assert.equal(goalFilter({ state: 'active' }), 'active')
  assert.equal((await c.openGoal('g1')).id, 'g1')
  assert.equal(calls.length, 1, 'a loaded goal opens without a call')
})

test('Back reopens a PR that a link or reload opened: the next screen pushes its own entry', async () => {
  const { c } = navigation()
  c.routeFix = true
  c.location.hash = prHash(B, 844)
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.equal(c.view, 'github')
  c.syncRoute()
  assert.deepEqual(c.entries, [], 'the PR address is already the entry')
  c.openView('files')
  c.syncRoute()
  assert.deepEqual(c.entries, [['push', `#room=${B}&view=files`]])
})

test('a shell link followed in the open room still opens its log', async () => {
  const { c } = navigation()
  const shell = 'sh_20261017-100000-aaaaaaaa'
  c.location.hash = detailHref({ kind: 'shell', id: shell }, B)
  assert.equal(c.hashTarget(), true)
  await setImmediate()
  assert.equal(c.view, 'jobs')
  assert.equal(c.openJob, shell)
})

test('a pairing link on a paired device keeps the open room; an empty address opens all rooms', async () => {
  const { c } = navigation()
  c.location.hash = '#pair=ABC'
  c.hashChanged()
  assert.equal(c.roomId, B)
  assert.equal(c.location.hash, '')
  c.hashChanged()
  await setImmediate()
  assert.equal(c.roomId, '')
})
