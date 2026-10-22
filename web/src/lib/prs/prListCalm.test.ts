import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { JSDOM } from 'jsdom'
import { domNames, bundle, importBundle } from './prDetailHarness.ts'
import type { Row } from './prlist.ts'
import { element, elements, need } from '../../../test/need.ts'

// The PR screen with an api that answers each call from globalThis.answers.
const API = "export const api = new Proxy({}, { get: (_, name) => () => Promise.resolve(globalThis.answers[name] ?? []) }); export const openExternal = () => {}"
// One build for the file; each test imports its own copy.
const built = await bundle({ component: 'GitHub', api: API })
const settle = () => new Promise<void>((r) => setTimeout(r, 0))
const row = (n: number): Row => ({ changeId: `c${n}`, key: `c${n}`, kind: 'github', number: n, title: `PR ${n}`, state: 'open', status: 'approved', ci: 'passed', verdict: 'approve', reviewState: 'approved', checks: [] })
const batch = (ids: number[]) => ids.map((n) => ({ changeId: `c${n}`, state: 'trial-running', trial: { branch: 'b', phase: 'running', batch: ids.map((i) => `c${i}`) } }))
const blocked = (ids: number[]) => ids.map((n) => ({ changeId: `c${n}`, state: 'conflict' }))

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
  Object.assign(globalThis, Object.fromEntries(names.map((n) => [n, n === 'window' ? w : Reflect.get(w, n)])), { answers: { roomMergeStatus: merge ?? [], githubList: { items: [], more: false }, ...answers } })
  const { dir, default: GitHub, mount, flushSync } = await importBundle(built, 'GitHub')
  const open = async (id = roomId, extra: object = {}) => {
    const app = need(document.getElementById('app'), '#app')
    app.replaceChildren()
    mount(GitHub, { target: app, props: { roomId: id, changes: [1, 2, 3].map(row), ...props, ...extra } })
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
    await rm(dir, { recursive: true, force: true })
  }
}
const toggle = (t: ParentNode, label: string) => elements(t, 'button.fold').find((b) => b.textContent?.includes(label))

test('queue and blocked rows are one line: title, number, author and one status dot', async () => {
  const red = { ...row(4), checks: [{ name: 'web', state: 'failure' }, { name: 'go', state: 'success' }] }
  const green = { ...row(1), checks: [{ name: 'web', state: 'success' }, { name: 'lint', state: 'skipped' }] }
  await screen(async ({ open, text }) => {
    const t = await open()
    const all = text(t)
    for (const noise of [/Trial batch/i, /Waiting \d/i, /Testing batch/, /Advisory/, /CI remains required/, /Conflict/]) assert.doesNotMatch(all, noise)
    assert.equal(t.querySelector('.pos'), null, 'no position numbers')
    const rows = elements(t, '.rows .row')
    assert.equal(rows.length, 4)
    for (const r of rows) assert.equal(r.querySelectorAll('.sdot').length, 1)
    const [first, , , last] = rows.map((r) => need(r))
    assert.equal(element(need(first), '.sdot').getAttribute('title'), 'Testing batch')
    assert.match(text(need(first)), /PR 1 ?#1/)
    assert.doesNotMatch(text(need(first)), /checks/, 'all green: no count')
    assert.equal(element(need(last), '.sdot').getAttribute('title'), 'Conflict')
    assert.match(text(need(last)), /1\/2 checks/, 'a red check shows the count')
  }, { merge: [...batch([1, 2, 3]), ...blocked([4])], props: { changes: [green, row(2), row(3), red] } })
})

test('the PR list has no reviewer bar, account name or reload button', async () => {
  await screen(async ({ open, text }) => {
    const t = await open()
    assert.doesNotMatch(text(t), /PR reviewer|as resident-labs/)
    assert.equal(t.querySelector('[aria-label="Reload"]'), null)
    assert.equal(t.querySelector('select'), null)
  }, { answers: { listServices: [{ role: 'reviewer', name: 'PR reviewer', pinned: 'sol' }], githubList: { items: [], more: false, account: 'octocat' } } })
})

test('Blocked is open by default and folds to a count', async () => {
  await screen(async ({ open, text, flushSync }) => {
    const t = await open()
    assert.match(text(t), /PR 1/)
    need(toggle(t, 'Blocked')).click()
    flushSync()
    assert.match(text(need(toggle(t, 'Blocked'))), /Blocked · 3/)
    assert.doesNotMatch(text(t), /PR 1/)
  }, { merge: blocked([1, 2, 3]) })
})

test('the choice is remembered for its room and wins over the batch default', async () => {
  await screen(async ({ open, text, flushSync }) => {
    let t = await open('room-a')
    need(toggle(t, 'Merge queue')).click()
    flushSync()
    t = await open('room-a')
    assert.doesNotMatch(text(t), /PR 1/, 'room-a keeps the queue folded')
    t = await open('room-b')
    assert.match(text(t), /PR 1/, 'room-b keeps the default')
  }, { merge: batch([1, 2, 3]) })
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
    assert.match(text(t), /No pull requests/)
    const end = t.querySelector('.end')
    assert.ok(end, 'the empty list still watches for older pages')
    need(observers.at(-1)).see()
    assert.equal(calls.length, 1)
  }, { props: { changes: [], older: true, onolder: () => calls.push(1) } })
})
