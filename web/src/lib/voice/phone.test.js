import { before, test } from 'node:test'
import assert from 'node:assert/strict'
import { capturing, initial, reduce } from './callState.js'
import { BUBBLE, DOUBLE_TAP_MS, HOLD_MS, bubbleGestures, bubbleOrb, bubbleTop, clampWidget, widgetPlace, bubblePlace, dragged, micControl, micModes, orb, phoneInitial, phoneLayout, phoneReduce, snapBubble } from './phone.js'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

const run = (...events) => events.reduce(reduce, initial)
const live = (...more) => run({ type: 'start', roomId: 'r1', room: 'ops' }, { type: 'started', startedAt: 1000 }, { type: 'voiceReady' }, ...more)

test('the phone starts a call as the bubble, opens it full screen on a tap, and shows nothing after it ends', () => {
  assert.equal(phoneLayout(initial, phoneInitial), 'none')
  assert.equal(phoneLayout(run({ type: 'start', roomId: 'r1', room: 'ops' }), phoneInitial), 'bubble', 'a call starts small')
  const full = phoneReduce(phoneInitial, { type: 'restore' })
  assert.equal(phoneLayout(live(), full), 'full')
  assert.equal(phoneLayout(live(), phoneReduce(full, { type: 'minimize' })), 'bubble')
  // A dropped call shows full screen with Call again, even as the bubble.
  assert.equal(phoneLayout(live({ type: 'closed', reason: '' }), phoneInitial), 'full')
  // Hanging up closes the screen back to the app.
  assert.equal(phoneLayout(live({ type: 'hangup' }, { type: 'ended' }), phoneInitial), 'none')
})

test('the sheet opens and closes, minimize closes it, and a new call starts as the bubble', () => {
  let ui = phoneReduce(phoneReduce(phoneInitial, { type: 'restore' }), { type: 'toggleSheet' })
  assert.deepEqual(ui, { minimized: false, sheet: true, resumeHidden: false })
  ui = phoneReduce(ui, { type: 'minimize' })
  assert.deepEqual(ui, { minimized: true, sheet: false, resumeHidden: false })
  assert.deepEqual(phoneReduce(ui, { type: 'newCall' }), phoneInitial)
  const full = phoneReduce(phoneInitial, { type: 'restore' })
  assert.equal(phoneReduce(full, { type: 'restore' }), full, 'no change, same state')
})

test('a dropped bubble snaps to the nearest side and stays clear of the top and bottom', () => {
  const screen = { width: 390, height: 844 }
  assert.deepEqual(snapBubble({ ...screen, x: 100, y: 422 }), { side: 'left', top: 0.5 })
  assert.deepEqual(snapBubble({ ...screen, x: 300, y: 422 }), { side: 'right', top: 0.5 })
  const margin = BUBBLE / 2 + 8
  assert.equal(snapBubble({ ...screen, x: 300, y: 0 }).top, Math.round((margin / 844) * 1000) / 1000)
  assert.equal(snapBubble({ ...screen, x: 300, y: 900 }).top, Math.round(((844 - margin) / 844) * 1000) / 1000)
})

test('a small move is a tap, a longer one drags, and a saved place is read back safely', () => {
  assert.equal(dragged({ x: 10, y: 10 }, 13, 14), false)
  assert.equal(dragged({ x: 10, y: 10 }, 20, 10), true)
  assert.deepEqual(bubblePlace(JSON.stringify({ side: 'left', top: 0.3 })), { side: 'left', top: 0.3 })
  for (const bad of [null, 'nope', '{"side":"up","top":0.3}', '{"side":"left","top":4}']) {
    assert.deepEqual(bubblePlace(bad), { side: 'right', top: 0.6 }, String(bad))
  }
})

// fakeClock runs the gestures' timers by hand, any number at once.
function fakeClock() {
  const timers = new Map()
  let next = 0
  const c = { t: 0 }
  c.set = (fn, ms) => (timers.set(++next, { fn, due: c.t + ms }), next)
  c.clear = (id) => timers.delete(id)
  c.now = () => c.t
  c.tick = (ms) => {
    c.t += ms
    for (const [id, x] of [...timers]) if (c.t >= x.due) { timers.delete(id); x.fn() }
  }
  c.pending = () => timers.size
  return c
}
function gestures() {
  const seen = []
  const clock = fakeClock()
  const g = bubbleGestures({ onmove: (p) => seen.push(['move', p.x]), ondrop: (p) => seen.push(['drop', p.x]), onexpand: () => seen.push(['open']), ontalk: (on) => seen.push(['talk', on]) }, clock)
  const tap = (x = 10) => { g.down({ button: 0, pointerId: 1, clientX: x, clientY: 10 }); clock.tick(80); g.up({ pointerId: 1, clientX: x, clientY: 10 }); g.click({ detail: 1 }) }
  return { g, seen, clock, tap }
}

test('one tap does nothing, a double tap opens the call, and a keyboard press opens it', () => {
  const { g, seen, clock, tap } = gestures()
  tap()
  assert.deepEqual(seen, [], 'one tap opens nothing')
  clock.tick(150)
  tap(12)
  assert.deepEqual(seen, [['open']])
  clock.tick(1000)
  tap()
  clock.tick(DOUBLE_TAP_MS + 1)
  tap()
  assert.deepEqual(seen, [['open']], 'taps far apart are two single taps')
  g.click({ detail: 0 }) // Enter, Space or assistive technology
  assert.deepEqual(seen, [['open'], ['open']])
})

test('a still hold talks until it lifts, and a drag never talks', () => {
  const { g, seen, clock } = gestures()
  const at = (x) => ({ button: 0, pointerId: 1, clientX: x, clientY: 10 })
  g.down(at(10))
  clock.tick(HOLD_MS - 1)
  assert.deepEqual(seen, [], 'not yet a hold')
  clock.tick(1)
  g.move(at(14)) // a still finger keeps talking
  g.up(at(14))
  assert.deepEqual(seen, [['talk', true], ['talk', false]])

  // A press that rests, then moves, drags: the owner's phone (msg_16b5434a69f55624).
  seen.length = 0
  g.down(at(10))
  clock.tick(HOLD_MS + 100)
  g.move(at(40))
  g.move(at(80))
  g.up(at(80))
  assert.deepEqual(seen, [['talk', true], ['talk', false], ['move', 40], ['move', 80], ['drop', 80]])

  seen.length = 0
  g.down(at(10))
  g.move(at(17))
  clock.tick(HOLD_MS * 2)
  g.move(at(60))
  g.up(at(60))
  assert.deepEqual(seen, [['move', 17], ['move', 60], ['drop', 60]])
})

// A browser throws "Illegal invocation" when setTimeout or Date.now runs as
// a method of another object. The real clock must call them on their own, or
// every press throws and the bubble neither drags nor talks.
test('the gestures run on the real timers the way a browser allows', () => {
  const real = { setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, now: Date.now }
  const strict = (f, self) => function (...a) {
    if (this !== undefined && this !== globalThis && this !== self) throw new TypeError('Illegal invocation')
    return f.apply(self, a)
  }
  globalThis.setTimeout = strict(real.setTimeout, globalThis)
  globalThis.clearTimeout = strict(real.clearTimeout, globalThis)
  Date.now = strict(real.now, Date)
  try {
    const seen = []
    const g = bubbleGestures({ onmove: (p) => seen.push(['move', p.x]), ondrop: (p) => seen.push(['drop', p.x]), onexpand: () => seen.push(['open']), ontalk: (on) => seen.push(['talk', on]) })
    assert.equal(g.down({ button: 0, pointerId: 1, clientX: 10, clientY: 10 }), true)
    g.move({ pointerId: 1, clientX: 60, clientY: 10 })
    g.up({ pointerId: 1, clientX: 60, clientY: 10 })
    assert.deepEqual(seen, [['move', 60], ['drop', 60]])
  } finally {
    globalThis.setTimeout = real.setTimeout
    globalThis.clearTimeout = real.clearTimeout
    Date.now = real.now
  }
})

// The bubble cancels its gesture when the page loses it or the bubble goes
// away mid-call, before or after the hold timer fires.
test('a cancel ends a hold, and a cancelled press never starts talking later', () => {
  const { g, seen, clock } = gestures()
  const at = { button: 0, pointerId: 1, clientX: 10, clientY: 10 }
  g.down(at)
  clock.tick(HOLD_MS)
  assert.equal(g.cancel(), true)
  assert.deepEqual(seen, [['talk', true], ['talk', false]])
  g.up(at)
  assert.equal(seen.length, 2, 'a hold or a drag is never a tap')

  seen.length = 0
  g.down(at)
  clock.tick(HOLD_MS - 1)
  g.cancel()
  clock.tick(HOLD_MS)
  assert.deepEqual(seen, [], 'the timer of a cancelled press never talks')
  assert.equal(clock.pending(), 0)
})

test('the first finger owns the gesture, and a second one changes nothing', () => {
  const { g, seen, clock } = gestures()
  const finger = (id) => ({ button: 0, pointerId: id, clientX: 10, clientY: 10 })
  assert.equal(g.down(finger(1)), true)
  assert.equal(g.down(finger(2)), false, 'a second finger is not taken')
  assert.equal(clock.pending(), 1, 'one hold timer')
  assert.equal(g.cancel(finger(2)), false, 'the second finger cannot cancel')
  g.up(finger(2))
  g.up(finger(1))
  clock.tick(HOLD_MS * 2)
  assert.deepEqual(seen, [], 'both lifted before the hold: nothing talks')

  g.down(finger(1))
  g.down(finger(2))
  clock.tick(HOLD_MS)
  g.up(finger(2))
  assert.deepEqual(seen, [['talk', true]], 'the second finger does not end the hold')
  g.up(finger(1))
  assert.deepEqual(seen, [['talk', true], ['talk', false]])
})

test('a hold opens a muted mic only while held, and leaves an open mic as it is', () => {
  const held = (s) => reduce(s, { type: 'hold', on: true })
  const release = (s) => reduce(s, { type: 'hold', on: false })
  const muted = live({ type: 'toggleMute' })
  assert.equal(capturing(held(muted)), true, 'a muted mic opens while held')
  assert.equal(release(held(muted)).muted, true, 'and mutes again on release')
  const open = live()
  assert.deepEqual(release(held(open)), open, 'open mic: a hold changes nothing')
  assert.equal(capturing(held(open)), true)
  const ptt = live({ type: 'pushToTalk', on: true })
  assert.equal(capturing(held(ptt)), true, 'push to talk talks while held')
  assert.deepEqual(release(held(ptt)), ptt)
  const pttMuted = reduce(ptt, { type: 'toggleMute' })
  assert.equal(capturing(held(pttMuted)), true)
  assert.equal(release(held(pttMuted)).muted, true)
  const deaf = live({ type: 'toggleDeafen' })
  assert.deepEqual(held(deaf), deaf, 'a deafened call stays silent')
})

test('a held bubble shows Voice listening only while the microphone is open', () => {
  const ptt = live({ type: 'pushToTalk', on: true }, { type: 'activity', value: 'speaking' })
  assert.equal(bubbleOrb(ptt, false).tone, 'speaking')
  assert.deepEqual(bubbleOrb(reduce(ptt, { type: 'talk', on: true }), true), { tone: 'listening', label: 'Listening' })
  assert.equal(bubbleOrb(live({ type: 'toggleMute' }), true).tone, 'muted', 'a held bubble never claims a muted microphone listens')
})

test('the orb shows the state by tone and label', () => {
  assert.deepEqual(orb(run({ type: 'start', roomId: 'r1', room: 'ops' })), { tone: 'idle', label: 'Starting Voice…' })
  assert.deepEqual(orb(live()), { tone: 'listening', label: 'Listening' })
  assert.deepEqual(orb(live({ type: 'activity', value: 'speaking' })), { tone: 'speaking', label: 'Speaking' })
  assert.deepEqual(orb(live({ type: 'activity', value: 'thinking' })), { tone: 'thinking', label: 'Thinking' })
  assert.deepEqual(orb(live({ type: 'toggleMute' })), { tone: 'muted', label: 'Muted' })
  assert.deepEqual(orb(live({ type: 'toggleDeafen' })), { tone: 'muted', label: 'Muted' })
  assert.deepEqual(orb(live({ type: 'pushToTalk', on: true })), { tone: 'muted', label: 'Muted' })
  assert.deepEqual(orb(live({ type: 'closed', reason: '' })), { tone: 'error', label: 'Call dropped' })
})

test('the one mic control: mute in open mic, hold to talk in push to talk', () => {
  assert.deepEqual(micControl(live()), { hold: false, pressed: false, label: 'Mute' })
  assert.deepEqual(micControl(live({ type: 'toggleMute' })), { hold: false, pressed: true, label: 'Unmute' })
  assert.deepEqual(micControl(live({ type: 'pushToTalk', on: true })), { hold: true, pressed: false, label: 'Hold to talk' })
  assert.deepEqual(micControl(live({ type: 'pushToTalk', on: true }, { type: 'talk', on: true })), { hold: true, pressed: true, label: 'Talking' })
})

test('a room switch asked from the bubble shows the full screen, and cancel goes back to the bubble', () => {
  const small = phoneInitial
  const asked = live({ type: 'askSwitch', roomId: 'r2', room: 'b' })
  assert.equal(phoneLayout(asked, small), 'full', 'the confirmation must be visible')
  assert.equal(phoneLayout(reduce(asked, { type: 'cancelSwitch' }), small), 'bubble')
})

test('the mic mode menu offers both modes and marks the current one', () => {
  assert.deepEqual(micModes(live()), [{ label: 'Open mic', on: false, current: true }, { label: 'Push to talk', on: true, current: false }])
  assert.deepEqual(micModes(live({ type: 'pushToTalk', on: true })).map((m) => m.current), [false, true])
})

// The full-screen call is an opaque modal above the app. Its mode menu must
// render inside it, or it opens behind the screen where no one can tap it.
test('the call screen renders its mode menu inside the dialog', () => {
  const ast = parse(readFileSync(new URL('./CallScreen.svelte', import.meta.url), 'utf8'), { modern: true })
  let found = false
  function walk(node, inDialog) {
    if (!node || typeof node !== 'object') return
    if (Array.isArray(node)) return node.forEach((n) => walk(n, inDialog))
    const here = inDialog || !!node.attributes?.some((a) => a.name === 'aria-modal')
    if (node.type === 'InlineComponent' || node.type === 'Component') {
      if (node.name === 'Menu') { assert.ok(here, 'Menu sits outside the dialog'); found = true }
    }
    for (const [k, v] of Object.entries(node)) if (k !== 'attributes') walk(v, here)
  }
  walk(ast.fragment, false)
  assert.ok(found, 'the call screen renders no mode menu')
})

test('on a phone, a call found on page load shows full screen to resume', () => {
  const s = reduce(initial, { type: 'status', available: true, live: true, roomId: 'r1', room: 'ops', startedAt: 1000 })
  assert.equal(phoneLayout(s, phoneReduce(phoneInitial, { type: 'minimize' })), 'full')
})

// A keyboard or assistive technology opens the call through the click, so
// the button must route clicks to the gestures.
test('the bubble button opens the call on its click', () => {
  const src = readFileSync(new URL('./VoiceBubble.svelte', import.meta.url), 'utf8')
  const button = src.slice(src.indexOf('<button'), src.indexOf('</button>'))
  assert.ok(button.includes('onclick={g.click}'), 'the button routes its click to the gestures')
})

test('the bubble never lets the page scroll or zoom under a finger', () => {
  const src = readFileSync(new URL('./VoiceBubble.svelte', import.meta.url), 'utf8')
  assert.match(src.slice(src.indexOf('.bubble {'), src.indexOf('}', src.indexOf('.bubble {'))), /touch-action: none;/)
})

// A hold whose pointerup never comes must still end: a lost capture, a
// blurred or hidden page, or the bubble going away while the call goes on.
test('the bubble ends its gesture whenever it loses the pointer or goes away', () => {
  const src = readFileSync(new URL('./VoiceBubble.svelte', import.meta.url), 'utf8')
  const button = src.slice(src.indexOf('<button'), src.indexOf('</button>'))
  for (const want of ['onpointercancel={cancel}', 'onlostpointercapture={cancel}']) assert.ok(button.includes(want), want)
  for (const want of ['$effect(() => () => cancel())', '<svelte:window onblur={() => cancel()} />', 'document.hidden && cancel()']) assert.ok(src.includes(want), want)
})

// A place saved in a bigger window, or on another screen, must not leave
// the call out of sight when the app loads in a smaller one.
test('a saved widget place is clamped into the window on load', () => {
  // The whole 300 by 96 px widget stays 8 px inside the window.
  assert.deepEqual(widgetPlace(JSON.stringify({ right: 1800, bottom: 1200 }), 1280, 800), { right: 972, bottom: 696 })
  assert.deepEqual(widgetPlace(JSON.stringify({ right: -40, bottom: 3 }), 1280, 800), { right: 8, bottom: 8 })
  assert.deepEqual(widgetPlace(JSON.stringify({ right: 300, bottom: 200 }), 1280, 800), { right: 300, bottom: 200 })
  for (const bad of [null, 'nope', '{"right":"x","bottom":1}']) assert.deepEqual(widgetPlace(bad, 1280, 800), { right: 16, bottom: 96 }, String(bad))
  // A measured, open widget.
  assert.deepEqual(clampWidget({ right: 900, bottom: 500 }, 1280, 800, { width: 360, height: 480 }), { right: 900, bottom: 312 })
  // A widget bigger than the window keeps its top left corner 8 px in, so
  // its header and controls stay reachable.
  const corner = (width, height, size) => {
    const p = clampWidget({ right: 500, bottom: 500 }, width, height, size)
    return { left: width - p.right - size.width, top: height - p.bottom - size.height }
  }
  assert.deepEqual(corner(400, 300, { width: 360, height: 480 }), { left: 8, top: 8 })
  assert.deepEqual(corner(250, 80, { width: 300, height: 96 }), { left: 8, top: 8 })
})

test('the bubble stays inside the top and bottom edges at any height', () => {
  assert.equal(bubbleTop({ side: 'right', top: 0.6 }), `clamp(8px, calc(60% - ${BUBBLE / 2}px), calc(100% - ${BUBBLE + 8}px))`)
  const src = readFileSync(new URL('./VoiceBubble.svelte', import.meta.url), 'utf8')
  assert.ok(src.includes('top:${bubbleTop(place)}'), 'the bubble places itself through bubbleTop')
})

const widgetRoom = 'room_20261020-000000-00000000'
let widgetCode
before(async () => {
  const api = `export const api = {
    voiceStatus: () => Promise.resolve({ available: true, live: true, roomId: '${widgetRoom}', room: 'ops', startedAt: 1000 }),
    feedTail: () => Promise.resolve({ posts: [] }),
    questions: () => Promise.resolve(globalThis.questionList ?? {}),
    voiceURL: () => 'ws://localhost/x',
  }`
  widgetCode = await bundle({ component: '../voice/VoiceWidget', api })
})

// The widget grows after it shows: its conversation opens, and a question
// from root arrives. Each size change places it again, whole on screen.
test('the widget stays whole on screen as it grows after opening and with a new question', { timeout: 1000 }, async () => {
  const { mock } = await import('node:test')
  // The resumable call re-reads its status on an interval; a mock interval
  // never keeps the test running.
  mock.timers.enable({ apis: ['setInterval'] })
  const { rm } = await import('node:fs/promises')
  const observers = []
  class FakeResizeObserver {
    constructor(cb) { this.cb = cb; observers.push(this) }
    observe() { this.cb([]) }
    disconnect() { observers.splice(observers.indexOf(this), 1) }
  }
  const dom = installDom({ ResizeObserver: FakeResizeObserver })
  const size = { width: 300, height: 96 }
  Object.defineProperty(window.HTMLElement.prototype, 'offsetWidth', { configurable: true, get() { return this.classList.contains('widget') ? size.width : 0 } })
  Object.defineProperty(window.HTMLElement.prototype, 'offsetHeight', { configurable: true, get() { return this.classList.contains('widget') ? size.height : 0 } })
  globalThis.localStorage = window.localStorage
  globalThis.HTMLMediaElement = window.HTMLMediaElement // ducking other media during a call
  globalThis.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} })
  window.localStorage.setItem('dyna.voice.widget', JSON.stringify({ right: 16, bottom: 660 }))
  const roomId = widgetRoom
  const question = { id: 'q1', state: 'open', to: 'owner', askerName: 'root', askedAt: '2026-10-20T09:00:00Z', text: 'Merge PR 7 now?', options: ['yes', 'no'] }
  const { dir, default: VoiceWidget, mount, unmount, flushSync } = await importBundle(widgetCode, '../voice/VoiceWidget')
  // until resolves when check holds for the DOM, after each change to it.
  const until = (check) => new Promise((resolve) => {
    if (check()) return resolve()
    const seen = new MutationObserver(() => { if (check()) { seen.disconnect(); resolve() } })
    seen.observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true })
  })
  const grow = (height) => { size.height = height; for (const o of [...observers]) o.cb([]); flushSync() }
  const top = () => { const w = document.querySelector('section.widget'); return window.innerHeight - parseFloat(w.style.bottom) - size.height }
  let app
  try {
    flushSync(() => { app = mount(VoiceWidget, { target: document.getElementById('app'), props: { narrow: false, onopenfile() {} } }) })
    await until(() => document.querySelector('section.widget'))
    assert.equal(top(), window.innerHeight - 660 - 96, 'the saved place fits')
    document.querySelector('button[aria-label="Show the conversation"]').click()
    flushSync()
    grow(400)
    assert.ok(top() >= 8, `the open conversation grew the widget out of the window: top ${top()}`)
    globalThis.questionList = { [roomId]: [question] }
    app.event({ kind: 'questions', roomId })
    await until(() => document.body.textContent.includes('Merge PR 7 now?'))
    grow(560)
    assert.ok(top() >= 8, `the question grew the widget out of the window: top ${top()}`)
  } finally {
    if (app) unmount(app)
    dom.window.close()
    for (const n of domNames) delete globalThis[n]
    delete globalThis.localStorage
    delete globalThis.HTMLMediaElement
    delete globalThis.matchMedia
    delete globalThis.questionList
    mock.timers.reset()
    await rm(dir, { recursive: true, force: true })
  }
})

// A call found on page load covers the phone. Hide or Escape collapses it to
// the bubble without touching the call; a double tap brings Rejoin back.
test('a call to rejoin can be hidden to the bubble and brought back, and the call is untouched', () => {
  const found = reduce(initial, { type: 'status', available: true, live: true, roomId: 'r1', room: 'ops', startedAt: 1000 })
  assert.equal(phoneLayout(found, phoneInitial), 'full', 'the call to rejoin shows first')
  const hidden = phoneReduce(phoneInitial, { type: 'hideResume' })
  assert.equal(phoneLayout(found, hidden), 'bubble')
  assert.equal(phoneLayout(found, phoneReduce(hidden, { type: 'restore' })), 'full', 'a double tap brings Rejoin back')
  const src = readFileSync(new URL('./CallScreen.svelte', import.meta.url), 'utf8')
  assert.ok(src.includes(`s.phase === 'resumable' ? onui({ type: 'hideResume' })`), 'Escape hides a call to rejoin')
  assert.ok(src.includes('aria-label="Hide the call" onclick={() => onui({ type: \'hideResume\' })}'), 'the call to rejoin has a Hide button')
})
