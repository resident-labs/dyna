import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { JSDOM } from 'jsdom'
import { compileModule } from 'svelte/compiler'
import { domNames, bundle, importBundle } from './prDetailHarness.ts'
import type { Row } from './prlist.ts'
import { element, elements, need } from '../../../test/need.ts'

// The PR screen with an api that answers each call from globalThis.answers.
const reactive = compileModule('export const versionState = $state({ mergeVersion: 0 }); globalThis.versionState = versionState;', { filename: 'version.svelte.js', dev: false }).js.code
const API = reactive + "\nexport const api = new Proxy({}, { get: (_, name) => () => Promise.resolve(globalThis.answers[name] ?? []) }); export const openExternal = () => {}"
// One build for the file; each test imports its own copy.
const built = await bundle({ component: 'GitHub', api: API })
const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve() }
const row = (n: number): Row => ({ changeId: `c${n}`, key: `c${n}`, kind: 'github', number: n, title: `PR ${n}`, state: 'open', status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', checks: [] })

// A stand-in for IntersectionObserver that a test fires by hand.
class FakeObserver {
  fn: (entries: { target: Element; isIntersecting: boolean }[]) => void
  on = new Set<Element>()
  constructor(fn: FakeObserver['fn']) { this.fn = fn }
  observe(n: Element) { this.on.add(n) }
  unobserve(n: Element) { this.on.delete(n) }
  disconnect() { this.on.clear() }
  see() { this.fn([...this.on].map((target) => ({ target, isIntersecting: true }))) }
}
interface Screen {
  open: (id?: string, extra?: object) => Promise<HTMLElement>
  observers: FakeObserver[]
  flushSync: () => void
  text: (t: Element) => string
  settle: () => Promise<void>
}
interface ScreenOptions {
  merge?: object[]
  props?: object
  roomId?: string
  storage?: Record<string, string>
  answers?: Record<string, unknown>
}
// observers collects every IntersectionObserver the screen makes.
async function screen(run: (screen: Screen) => Promise<void>, { merge, props = {}, roomId = 'r', storage, answers = {} }: ScreenOptions = {}) {
  const dom = new JSDOM('<!doctype html><div id="app"></div>', { pretendToBeVisual: true, url: 'http://localhost/' })
  const w = dom.window
  const observers: FakeObserver[] = []
  class Observer extends FakeObserver {
    constructor(fn: FakeObserver['fn']) { super(fn); observers.push(this) }
  }
  Object.assign(w, { ResizeObserver: class { observe() {} unobserve() {} disconnect() {} }, IntersectionObserver: Observer })
  Object.assign(w.Element.prototype, { animate() {
    const a = { currentTime: 0, cancel() {}, finished: Promise.resolve() }
    Object.defineProperty(a, 'onfinish', { set(fn: (() => void) | null) { if (fn) queueMicrotask(fn) } })
    return a
  } })
  if (storage) for (const [k, v] of Object.entries(storage)) w.localStorage.setItem(k, v)
  const names = [...domNames, 'location', 'history', 'localStorage']
  Object.assign(globalThis, Object.fromEntries(names.map((n) => [n, n === 'window' ? w : Reflect.get(w, n)])), { answers: { mergeControlStatus: { roomId, paused: false, queue: merge ?? [], trials: [], localCIChecks: {} }, githubList: { items: [], more: false }, ...answers } })
  const { dir, default: GitHub, mount, flushSync } = await importBundle(built, 'GitHub')
  const open = async (id = roomId, extra: object = {}) => {
    const app = need(document.getElementById('app'), '#app')
    app.replaceChildren()
    mount(GitHub, { target: app, props: { get mergeVersion() { return Reflect.get(globalThis, 'versionState').mergeVersion }, roomId: id, changes: [1, 2, 3].map(row), ...props, ...extra } })
    await settle()
    flushSync()
    return app
  }
  try {
    await run({ open, observers, flushSync, text: (t: Element) => (t.textContent ?? '').replace(/\s+/g, ' '), settle })
  } finally {
    dom.window.close()
    for (const n of names) Reflect.deleteProperty(globalThis, n)
    Reflect.deleteProperty(globalThis, 'answers')
    Reflect.deleteProperty(globalThis, 'versionState')
    await rm(dir, { recursive: true, force: true })
  }
}
test('the room list shows only open PRs with their next action', async () => {
  await screen(async ({ open, text }) => {
    const t = await open()
    const rows = elements(t, '.rows .row')
    assert.equal(rows.length, 2)
    assert.match(text(need(rows[0])), /PR 1.*#1.*Ready to merge/)
    assert.match(text(need(rows[1])), /PR 2/)
    assert.doesNotMatch(text(t), /PR 3|PR 5|Local change/)
    assert.equal(t.querySelector('.dscroll'), null, 'opening the list does not select a PR')
    assert.equal(t.querySelector('[aria-label="State"]'), null)
  }, { merge: [{ changeId: 'c1', state: 'ready' }], props: { changes: [
    { ...row(1), head: 'abc', reviewHead: 'abc' }, row(2),
    { ...row(3), state: 'merged' }, { ...row(4), kind: 'local', title: 'Local change' }, { ...row(5), state: 'closed' },
  ] } })
})

test('a row opens the existing detail and Back returns to the list', async () => {
  await screen(async ({ open, flushSync }) => {
    const t = await open()
    element(t, '.rows .row').click()
    flushSync()
    assert.ok(t.querySelector('.dscroll'))
    const back = t.querySelector<HTMLButtonElement>('button.back')
    let wentBack = false
    history.back = () => {
      wentBack = true
      history.replaceState(null, '', '/')
      window.dispatchEvent(new window.PopStateEvent('popstate'))
    }
    need(back, 'Back button').click()
    flushSync()
    assert.equal(wentBack, true)
    assert.equal(t.querySelector('.dscroll'), null)
    assert.equal(elements(t, '.rows .row').length, 3)
  })
})

test('a trial PR outside the loaded page opens through the existing PR route', async () => {
  const opened: number[] = []
  await screen(async ({ open, flushSync }) => {
    const t = await open()
    element(t, '.merging .batch button').click()
    flushSync()
    assert.deepEqual(opened, [99])
  }, { props: { onpr: (number: number) => opened.push(number) }, answers: { mergeControlStatus: {
    roomId: 'r', paused: false, queue: [], trials: [{ branch: 'dyna/merge-trial/1', phase: 'running', batch: ['github.com/o/r#99'] }], localCIChecks: {},
  } } })
})

test('mergeVersion refreshes main CI without a changed PR row', async () => {
  await screen(async ({ open, text, flushSync, settle }) => {
    const t = await open()
    assert.match(text(t), /Waiting for default branch CI/)
    Reflect.get(globalThis, 'answers').mergeControlStatus = {
      roomId: 'r', paused: false, queue: [{ changeId: 'c1', state: 'ready', main: { state: 'passed' } }], trials: [], localCIChecks: {},
    }
    Reflect.get(globalThis, 'versionState').mergeVersion++
    flushSync()
    await settle()
    flushSync()
    assert.doesNotMatch(text(t), /Waiting for default branch CI|Merge status unavailable|Loading merge status/)
    assert.match(text(t), /No merge running/)
    assert.equal(elements(t, '.rows .row').length, 3)
  }, { merge: [{ changeId: 'c1', state: 'ready', main: { state: 'pending', head: 'e1d554e3f2a52ea46a2d575803f412b2cbac2514', wait: { kind: 'pending', base: 'main' } } }] })
})

test('the PR list has no reviewer bar, account name or reload button', async () => {
  await screen(async ({ open, text }) => {
    const t = await open()
    assert.doesNotMatch(text(t), /PR reviewer|as resident-labs/)
    assert.equal(t.querySelector('[aria-label="Reload"]'), null)
    assert.equal(t.querySelector('select'), null)
  }, { answers: { listServices: [{ role: 'reviewer', name: 'PR reviewer', pinned: 'sol' }], githubList: { items: [], more: false, account: 'octocat' } } })
})

test('older pull requests load when the end of the list comes into view', async () => {
  const calls: number[] = []
  await screen(async ({ open, observers, text, settle }) => {
    const t = await open()
    const rows = element(t, '.rows')
    assert.equal(element(t, '.lhead').querySelector('.load-older'), null)
    assert.equal(t.querySelector('.list > .load-older'), null)
    const end = rows.querySelector('.end')
    assert.ok(end, 'the end of the list watches for older pages')
    assert.equal(rows.lastElementChild, end)
    assert.equal(rows.querySelector('button.load-older'), null, 'no button while auto-load works')
    need(observers.at(-1)).see()
    assert.equal(calls.length, 1)
  }, { props: { older: true, onolder: () => calls.push(1) }, merge: [] })
})

test('older loading makes no second request while one runs, or when nothing is older', async () => {
  const calls: number[] = []
  await screen(async ({ open, observers }) => {
    await open()
    const t = need(document.getElementById('app'), '#app')
    for (const o of observers) o.see()
    assert.equal(calls.length, 0)
    assert.match(element(t, '.end').textContent ?? '', /Loading older/)
    assert.equal(t.querySelector('button.load-older'), null)
  }, { props: { older: true, loadingOlder: true, onolder: () => calls.push(1) } })
  await screen(async ({ open, observers, text }) => {
    const t = await open()
    for (const o of observers) o.see()
    assert.equal(t.querySelector('.end, .load-older'), null)
    assert.equal(calls.length, 0)
  }, { props: { older: false, onolder: () => calls.push(1) } })
})

test('after a failed load the end shows a Retry button and stops auto-loading', async () => {
  const calls: number[] = []
  await screen(async ({ open, observers }) => {
    const t = await open()
    for (const o of observers) o.see()
    assert.equal(calls.length, 0)
    assert.equal(t.querySelector('.end'), null)
    const retry = element(t, '.rows button.load-older')
    assert.match(retry.textContent ?? '', /Retry/)
    retry.click()
    assert.equal(calls.length, 1)
  }, { props: { older: true, failedOlder: true, onolder: () => calls.push(1) } })
})

test('older pages still load when the filter hides every loaded row', async () => {
  const calls: number[] = []
  await screen(async ({ open, observers, text }) => {
    const t = await open()
    assert.match(text(t), /No tracked pull requests/)
    const end = t.querySelector('.end')
    assert.ok(end, 'the empty list still watches for older pages')
    need(observers.at(-1)).see()
    assert.equal(calls.length, 1)
  }, { props: { changes: [], older: true, onolder: () => calls.push(1) } })
})

// A busy upstream room opens on what it tracks. All on GitHub brings back
// every open PR, the search filters the current view, and each room keeps
// its own choice.
test('the PR list shows tracked PRs first and remembers All on GitHub per room', async () => {
  const github = [41, 42].map((number) => ({ number, title: `Upstream ${number}`, state: 'OPEN', author: { login: 'x' }, labels: [], workingSessionLive: false, createdAt: '', updatedAt: '', url: `https://github.com/o/r/pull/${number}` }))
  await screen(async ({ open, text, flushSync, settle }) => {
    let t = await open()
    assert.deepEqual(elements(t, '.rows .row-title').map((r) => text(r).trim()), ['PR 3', 'PR 2', 'PR 1'])
    assert.match(text(t), /3 tracked/)
    const search = element<HTMLInputElement>(t, 'input.search')
    search.value = 'PR 2'
    search.dispatchEvent(new window.Event('input'))
    flushSync()
    assert.deepEqual(elements(t, '.rows .row-title').map((r) => text(r).trim()), ['PR 2'])
    need(elements(t, '[aria-label="Show"] button').find((b) => b.textContent === 'All on GitHub')).click()
    await settle()
    flushSync()
    search.value = ''
    search.dispatchEvent(new window.Event('input'))
    flushSync()
    assert.match(text(t), /Upstream 42.*Upstream 41/)
    assert.match(text(t), /5 open/)
    t = await open()
    assert.match(text(t), /Upstream 42/, 'the room remembers All on GitHub')
    t = await open('other')
    assert.doesNotMatch(text(t), /Upstream/, 'another room keeps its own choice')
  }, { answers: { githubList: { repo: 'o/r', items: github, more: false } } })
})
