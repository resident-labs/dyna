import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'

type Mounted = object
// The harness is plain JavaScript, so its exports are typed here at the boundary.
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => Mounted
  unmount: (app: Mounted) => void
  flushSync: (fn?: () => void) => void
}
type Page = { root: HTMLElement; flushSync: Harness['flushSync']; done: () => Promise<void> }

// need returns the value, or throws when a test expects it to exist.
function need<T>(value: T | null | undefined, what: string): T {
  if (value === null || value === undefined) throw new Error(`missing ${what}`)
  return value
}
const textOf = (node: Node): string => need(node.textContent, 'text')

const now = Date.parse('2026-10-21T22:55:00Z')
// The clock stands still at the fixture's now, so the page's timers and
// durations are the same in every test.
mock.timers.enable({ apis: ['Date'], now })
const format = readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8')

// The fixture answers every call the page makes, with the room's real shape.
// A write records itself on the shared global, because the page runs as its own
// module instance.
declare global { var __writes: string[] }
const writes = (): string[] => globalThis.__writes ?? []
const api = `
const now = ${now}
const iso = (minutesAgo) => new Date(now - minutesAgo * 60000).toISOString()
export const api = {
  listServices: () => Promise.resolve([
    { role: 'reviewer', name: 'PR reviewer', profile: 'sol', pinned: 'sol', reason: '', profiles: ['luna', 'sol'] },
    { role: 'merge-steward', name: 'Merge steward', profile: 'off', pinned: 'off', reason: '', profiles: ['luna', 'sol'] },
  ]),
  reviewerStatus: () => Promise.resolve({ enabled: true, profile: 'sol', lastError: '', queue: [], reviewers: [{ current: { pr: 1943, title: 'bundle only the DM Session' } }], recent: [{ pr: 1918, verdict: 'approve', at: iso(1) }] }),
  mergeControlStatus: () => Promise.resolve({ paused: false, queue: [{ state: 'trial-running', reason: 'running (3 of 4 jobs done)', trial: { branch: 'batch-a', batch: ['a', 'b'] } }, { state: 'held', reason: 'draft' }] }),
  roomMergeHistory: () => Promise.resolve([{ kind: 'publish', outcome: 'published', at: iso(10), changes: ['PR #1919', 'PR #1933'], detail: '' }]),
  settings: () => Promise.resolve([{ key: 'library.enabled', value: ['/repo'] }, { key: 'library.profile', value: 'sonnet' }, { key: 'runs.retention', value: '336h0m0s' }, { key: 'worktrees.retention', value: 'changed' }]),
  usageRollup: () => Promise.resolve({ byRoleModel: [{ role: 'reviewer', turns: 107, cost: { usd: null } }, { role: 'librarian', turns: 7, cost: { usd: 1.04 } }] }),
  voiceStatus: () => Promise.resolve({ available: true, live: false }),
  hookSummary: () => Promise.resolve([
    { hook: { name: 'self-update', label: 'Update Dyna', event: 'schedule', everyMinutes: 60, disabled: false }, lastRun: { result: 'pass', startedAt: iso(6), finishedAt: iso(5.4) } },
    { hook: { name: 'librarian-batch', label: 'Librarian', event: 'schedule', everyMinutes: 10, disabled: false }, lastRun: { result: 'pass', startedAt: iso(4), finishedAt: iso(3.9) } },
  ]),
  notices: () => Promise.resolve([{ key: 'provider:sol', severity: 'warn', text: 'sol: provider limit reached, resets in 60m' }]),
  roomCIBudget: () => Promise.resolve({ ciWarnAfter: '3m0s', ciFailAfter: '4m0s', ciTestMax: '1s' }),
  accountUsageCached: () => Promise.resolve({ accounts: [{ observedAt: iso(12), stale: true }] }),
  ciHealth: () => Promise.resolve({ limitMs: 1000, tests: [{ test: 'TestWorse', limitMs: 1000, runs: [{ durationMs: 1200 }, { durationMs: 900 }] }] }),
  setReviewEnabled: (room, on) => { globalThis.__writes = (globalThis.__writes || []).concat('review.enabled ' + on); return Promise.reject(new Error('engine said no')) },
}
export const openExternal = () => {}
`

let code: Uint8Array | null = null
const build = async (): Promise<Uint8Array> => (code ??= await bundle({ component: '../services/Services', api, format }))

async function mountPage(extra: object = {}): Promise<Page> {
  const dom = installDom()
  const { dir, default: C, mount, unmount, flushSync }: Harness = await importBundle(await build(), '../services/Services')
  const root = need(document.getElementById('app'), 'app element')
  let app: Mounted | undefined
  const props = { roomId: 'room_1', repo: '/repo', members: [], changes: [], ...extra }
  flushSync(() => { app = mount(C, { target: root, props }) })
  const done = async () => { unmount(need(app, 'mounted page')); dom.window.close(); for (const n of domNames) Reflect.deleteProperty(globalThis, n); await rm(dir, { recursive: true, force: true }) }
  return { root, flushSync, done }
}

// settle lets the fixture promises resolve and the page render their results.
const settle = async (page: Page) => {
  for (let i = 0; i < 8; i++) {
    await new Promise((r) => setTimeout(r, 0))
    page.flushSync()
  }
}

const serviceRow = (root: ParentNode, name: string): HTMLElement | undefined =>
  [...root.querySelectorAll<HTMLElement>('.list .row')].find((row) => textOf(row).includes(name))

test('every service gets a line with its switch or its reason, and the timed ones list next', async () => {
  const page = await mountPage()
  try {
    await settle(page)
    const list = need(page.root.querySelector('.list'), 'services list')
    for (const name of ['PR reviewers', 'Merge queue', 'Merge steward', 'Conflict radar', 'Librarian', 'Update Dyna', 'Maintenance', 'Account usage', 'CI admission', 'Voice']) {
      assert.ok(serviceRow(list, name), `missing ${name}`)
    }
    const reviewer = textOf(need(serviceRow(list, 'PR reviewers'), 'reviewer row'))
    assert.match(reviewer, /reviewing #1943/)
    assert.match(reviewer, /sol/)
    assert.match(reviewer, /rate-limited/)
    assert.match(reviewer, /107 turns/)
    // A service with no switch in the engine says so instead of showing a dead one.
    assert.match(textOf(need(serviceRow(list, 'Maintenance'), 'maintenance row')), /no switch/)
    assert.doesNotMatch(textOf(need(serviceRow(list, 'Merge queue'), 'merge row')), /no switch/)

    const next = textOf(need(page.root.querySelector('.next'), 'scheduled next'))
    assert.match(next, /Update Dyna/)
    assert.match(next, /in \d+ m/)
  } finally { await page.done() }
})

test('the timeline folds a review into one line, and tapping a service filters it', async () => {
  const page = await mountPage({
    changes: [{ number: 1918, title: 'skip repeated Watch work', verdict: 'approve', reviewAt: new Date(now - 60000).toISOString(), reviewerSession: 'ses_r' }],
  })
  try {
    await settle(page)
    const mid = need(page.root.querySelector('.mid'), 'activity')
    assert.match(textOf(mid), /approve #1918/)
    assert.match(textOf(mid), /published main · 2 changes/)
    const before = mid.querySelectorAll('.tl .it').length
    assert.ok(before >= 4, `lines ${before}`)
    const mergeLine = [...mid.querySelectorAll<HTMLElement>('.tl .it')].find((li) => textOf(li).includes('published main'))
    need(mergeLine, 'merge line').querySelector<HTMLElement>('.svc')?.click()
    page.flushSync()
    const filtered = page.root.querySelectorAll('.tl .it').length
    assert.ok(filtered > 0 && filtered < before, `filtered ${filtered} of ${before}`)
    assert.match(textOf(need(page.root.querySelector('.scope'), 'scope')), /Showing/)
    const showAll = [...page.root.querySelectorAll<HTMLElement>('.scope button')].find((b) => textOf(b) === 'show all')
    need(showAll, 'show all').click()
    page.flushSync()
    assert.equal(page.root.querySelectorAll('.tl .it').length, before)
  } finally { await page.done() }
})

test('a switch writes through its call, and a refusal shows on its own line', async () => {
  globalThis.__writes = []
  const page = await mountPage()
  try {
    await settle(page)
    const reviewer = need(serviceRow(page.root, 'PR reviewers'), 'reviewer row')
    const toggle = need(reviewer.querySelector<HTMLElement>('[role="switch"]'), 'reviewer switch')
    assert.equal(toggle.getAttribute('aria-checked'), 'true')
    toggle.click()
    await settle(page)
    assert.deepEqual(writes(), ['review.enabled false'])
    assert.match(textOf(reviewer), /engine said no/)
    // The switch keeps the engine's value: the write failed, so it stays on.
    assert.equal(need(reviewer.querySelector('[role="switch"]'), 'reviewer switch').getAttribute('aria-checked'), 'true')
  } finally { await page.done() }
})

test('the test-health line is one plain line above the timeline, and opens the CI view', async () => {
  let opened = 0
  const page = await mountPage({ onopenhealth: () => { opened++ } })
  try {
    await settle(page)
    // The line is the link itself, so a thumb hits it on the first tap.
    const line = need(page.root.querySelector<HTMLElement>('.mid button.health'), 'test-health link')
    assert.equal(textOf(line), 'Slow tests · 1 test over 1 s')
    line.click()
    assert.equal(opened, 1)
    // The line comes before the first timeline row, so it reads above it.
    const first = need(page.root.querySelector('.mid .tl .it'), 'first timeline row')
    assert.equal(line.compareDocumentPosition(first) & 4, 4, 'the line sits above the timeline')
  } finally { await page.done() }
})
