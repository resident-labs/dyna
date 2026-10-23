import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import type { Member } from '../api/room'
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
const one = <E extends Element = HTMLElement>(root: ParentNode, selector: string): E => need(root.querySelector<E>(selector), selector)
const textOf = (node: Node): string => need(node.textContent, 'text')

const format = readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8')
// The page reads the clock for live durations, so every mounted page sees this instant.
const now = Date.parse('2026-01-01T01:00:00Z')
const iso = (ms: number): string => new Date(now - ms).toISOString()
// The fixtures set only the fields the page reads. The rest take neutral values the shared Member type requires.
const unset = { resumable: false, address: '', depth: 0, children: 0, status: '', state: '', turns: 0, created: '', lastActivity: '', hue: 0 }
const members: Member[] = [
  { ...unset, owner: true, name: 'repo' },
  { ...unset, id: 's1', name: 'lead', address: 'repo:lead', hue: 1, state: 'live', status: 'running', created: iso(600000), lastActivity: iso(1000), doing: { text: 'Bash: go test', since: now - 45000 } },
  { ...unset, id: 's2', name: 'old', address: 'repo:old', hue: 2, state: 'closed', status: 'closed', closed: true, created: iso(900000), ended: iso(800000), lastActivity: iso(800000) },
]
const api = `export const api = { goalJournal: () => Promise.resolve({ items: [{ id: 'j1', at: ${JSON.stringify(iso(5000))}, needsAction: false, entries: [{ agent: 'lead' }] }], older: false }), goalEvents: () => Promise.resolve({ items: [{ id: 'e1', at: ${JSON.stringify(iso(5000))}, goal: 'g1', by: 's1', event: 'note', text: 'holder note' }], older: false }) }; export const openExternal = () => {}`

// Build the page once; each test imports it into its own DOM.
const code = await bundle({ component: '../services/Services', api, format })

async function mountPage(props: { members: Member[]; changes: unknown[]; onopen: (m: Member) => void }): Promise<Page> {
  mock.timers.enable({ apis: ['Date'], now })
  const dom = installDom()
  const { dir, default: C, mount, unmount, flushSync }: Harness = await importBundle(code, '../services/Services')
  const root = need(document.getElementById('app'), 'app element')
  let app: Mounted | undefined
  flushSync(() => { app = mount(C, { target: root, props }) })
  const done = async () => { unmount(need(app, 'mounted page')); dom.window.close(); mock.timers.reset(); for (const n of domNames) Reflect.deleteProperty(globalThis, n); await rm(dir, { recursive: true, force: true }) }
  return { root, flushSync, done }
}

test('the page lists events and current sessions with the live tool line; earlier stays out until opened', async (t) => {
  // The clock stands still at the fixture's now, so a slow mount cannot add seconds to "45s".
  t.mock.method(Date, 'now', () => now)
  const page = await mountPage({ members, changes: [], onopen: () => {} })
  try {
    await new Promise((r) => setTimeout(r, 0))
    page.flushSync()
    const text = textOf(page.root)
    assert.match(text, /lead\s*started/)
    assert.match(page.root.innerHTML, /class="doing[^"]*long[^"]*"[^>]*>Bash: go test · 45s</)
    assert.doesNotMatch(textOf(one(page.root, '.sessions')), /old/)
    one<HTMLButtonElement>(page.root, 'button.earlier').click()
    page.flushSync()
    assert.match(textOf(one(page.root, '.sessions')), /old/)
  } finally { await page.done() }
})

test('clicking an event or a session opens that session', async () => {
  const opened: (string | undefined)[] = []
  const page = await mountPage({ members, changes: [], onopen: (m) => opened.push(m.id) })
  try {
    need([...page.root.querySelectorAll<HTMLElement>('.ev')].find((b) => textOf(b).includes('lead')), 'lead event').click()
    one<HTMLElement>(page.root, '.sessions .ses').click()
    assert.deepEqual(opened, ['s1', 's1'])
  } finally { await page.done() }
})

// A real engine holds thousands of sessions. The page must render a bounded window of them.
const crowd: Member[] = [{ ...unset, owner: true, name: 'repo' }, ...Array.from({ length: 900 }, (_, i): Member => ({ ...unset, id: 'c' + i, name: 'agent-' + i, address: 'repo:agent-' + i, hue: i % 8, state: 'idle', status: i % 3 ? 'closed' : 'idle', closed: i % 3 !== 0, created: iso(1000 + i * 60000), ended: i % 3 ? iso(500 + i * 30000) : undefined, lastActivity: iso(100 + i * 1000) }))]

test('thousands of sessions render a bounded number of rows; more load on request', async () => {
  const page = await mountPage({ members: crowd, changes: [], onopen: () => {} })
  try {
    const count = (sel: string): number => page.root.querySelectorAll(sel).length
    assert.ok(count('.events .ev') <= 200, `events ${count('.events .ev')}`)
    assert.ok(count('.sessions .ses') <= 200, `sessions ${count('.sessions .ses')}`)
    assert.ok(count('.events .ev') > 0 && count('.sessions .ses') > 0)
    one<HTMLButtonElement>(page.root, '.events .more').click()
    page.flushSync()
    assert.ok(count('.events .ev') > 200 && count('.events .ev') <= 400, `events after more ${count('.events .ev')}`)
    one<HTMLButtonElement>(page.root, 'button.earlier').click()
    page.flushSync()
    assert.ok(count('.sessions .ses') <= 400, `with earlier ${count('.sessions .ses')}`)
  } finally { await page.done() }
})
