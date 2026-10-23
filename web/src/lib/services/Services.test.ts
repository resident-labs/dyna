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
  ]),
  reviewerStatus: () => Promise.resolve({ enabled: true, profile: 'sol', lastError: '', queue: [], reviewers: [{ current: { pr: 1943, title: 'bundle only the DM Session' } }], recent: [{ pr: 1918, verdict: 'approve', at: iso(1) }] }),
  mergeControlStatus: () => Promise.resolve({ paused: false, queue: [{ state: 'trial-running', reason: 'running (3 of 4 jobs done)', trial: { branch: 'batch-a', batch: ['a', 'b'] } }, { state: 'held', reason: 'draft' }] }),
  roomMergeHistory: () => Promise.resolve([{ kind: 'publish', outcome: 'published', at: iso(10), branch: 'dyna/merge-trial/5', head: 'abcdef0123', changes: ['PR #1919', 'PR #1933'], detail: '' }, { kind: 'trial', outcome: 'passed', at: iso(11), branch: 'dyna/merge-trial/5', head: 'abcdef0', changes: ['PR #1919', 'PR #1933'], detail: '' }]),
  settings: () => Promise.resolve([{ key: 'library.enabled', value: ['/repo'] }, { key: 'library.profile', value: 'sonnet' }, { key: 'runs.retention', value: '336h0m0s' }, { key: 'worktrees.retention', value: 'changed' }]),
  usageRollup: () => Promise.resolve({ byRoleModel: [{ role: 'reviewer', turns: 107, cost: { usd: null } }, { role: 'librarian', turns: 7, cost: { usd: 1.04 } }] }),
  voiceStatus: () => Promise.resolve({ available: true, live: false }),
  hookSummary: () => Promise.resolve([
    { hook: { name: 'self-update', label: 'Update Dyna', event: 'schedule', everyMinutes: 60, disabled: false }, lastRun: { result: 'pass', startedAt: iso(6), finishedAt: iso(5.4) } },
    { hook: { name: 'librarian-batch', label: 'Librarian', event: 'schedule', everyMinutes: 10, disabled: false }, lastRun: { result: 'pass', startedAt: iso(4), finishedAt: iso(3.9) } },
  ]),
  hookRuns: (repo, name) => Promise.resolve(name === 'librarian-batch' ? [4, 14, 24].map((m) => ({ result: 'pass', startedAt: iso(m), finishedAt: iso(m - 0.02) })) : []),
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

const tabButton = (root: ParentNode, name: string): HTMLElement =>
  need([...root.querySelectorAll<HTMLElement>('[role="tab"]')].find((t) => textOf(t).includes(name)), `tab ${name}`)

const open = (page: Page, name: string): HTMLElement => {
  tabButton(page.root, name).click()
  page.flushSync()
  return need(page.root.querySelector<HTMLElement>('[role="tabpanel"]'), 'tab panel')
}

test('the page has one tab per service family, and More holds the services with no results', async () => {
  const page = await mountPage()
  try {
    await settle(page)
    const names = [...page.root.querySelectorAll('[role="tab"]')].map((t) => textOf(t).trim())
    assert.deepEqual(names, ['Merges', 'Reviews', 'Updates and hooks', 'Librarian', 'CI health', 'More'])
    const more = textOf(open(page, 'More'))
    for (const name of ['Conflict radar', 'Maintenance', 'Account usage', 'Voice']) assert.match(more, new RegExp(name))
    assert.doesNotMatch(more, /no switch|always on/)
  } finally { await page.done() }
})

test('the merges tab groups a trial with its publish, and the librarian tab folds its runs', async () => {
  const page = await mountPage()
  try {
    await settle(page)
    const merges = textOf(open(page, 'Merges'))
    assert.match(merges, /Trial 5 · PR #1919, PR #1933/)
    assert.match(merges, /published main · abcdef0/)
    assert.equal(page.root.querySelectorAll('.tl .it').length, 1, 'one entry for the trial and its publish')
    const librarian = open(page, 'Librarian')
    assert.match(textOf(librarian), /Librarian · 3 runs passed today/)
    assert.equal(librarian.querySelectorAll('.tl .it').length, 1)
  } finally { await page.done() }
})

test('the reviews tab states a provider limit once and keeps its controls on the header line', async () => {
  globalThis.__writes = []
  const page = await mountPage({
    changes: [{ number: 1918, title: 'skip repeated Watch work', verdict: 'approve', reviewAt: new Date(now - 60000).toISOString(), reviewerSession: 'ses_r' }],
  })
  try {
    await settle(page)
    const panel = open(page, 'Reviews')
    const head = textOf(need(panel.querySelector('.row'), 'reviewer line'))
    assert.match(head, /Rate-limited/)
    assert.equal((head.match(/rate-limited/gi) ?? []).length, 1)
    assert.match(textOf(panel), /approve #1918/)
    const toggle = need(panel.querySelector<HTMLElement>('[role="switch"]'), 'reviewer switch')
    toggle.click()
    await settle(page)
    assert.deepEqual(writes(), ['review.enabled false'])
    assert.match(textOf(panel), /engine said no/)
    assert.equal(need(panel.querySelector('[role="switch"]'), 'reviewer switch').getAttribute('aria-checked'), 'true')
  } finally { await page.done() }
})

test('the CI health tab shows the slow-test state and opens the CI view', async () => {
  let opened = 0
  const page = await mountPage({ onopenhealth: () => { opened++ } })
  try {
    await settle(page)
    const panel = open(page, 'CI health')
    assert.match(textOf(panel), /1 test over 1 s/)
    need([...panel.querySelectorAll<HTMLElement>('button.health')][0], 'open link').click()
    assert.equal(opened, 1)
  } finally { await page.done() }
})
