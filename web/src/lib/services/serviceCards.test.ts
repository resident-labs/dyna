import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import test from 'node:test'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'

const format = readFileSync(new URL('../ui/format.js', import.meta.url), 'utf8')
const now = Date.now()
const iso = (ms: number) => new Date(now - ms).toISOString()
const members = [
  { owner: true, name: 'repo' },
  { id: 'r1', name: 'reviewer-9', role: 'reviewer', service: true, hue: 1, state: 'idle', status: 'idle', created: iso(600000), lastActivity: iso(1000) },
  { id: 'k1', name: 'keeper', role: 'keeper', service: true, hue: 3, state: 'idle', status: 'idle', created: iso(900000), lastActivity: iso(2000) },
]
const changes = [{ key: 'r#9', number: 9, reviewerSession: 'r1', reviewerName: 'reviewer-9', verdict: 'approve', reviewAt: iso(300000) }]

// The page calls the engine through api: record the control calls.
const calls: unknown[][] = []
Object.assign(globalThis, { recordCall: (c: unknown[]) => calls.push(c) })
const api = `export const api = {
  goalJournal: () => Promise.resolve({ items: [], older: false }),
  goalEvents: () => Promise.resolve({ items: [], older: false }),
  listServices: () => { globalThis.recordCall(['listServices']); return Promise.resolve([{ role: 'reviewer', profile: 'opus', pinned: '', reason: '', profiles: ['opus'] }]) },
  reviewerStatus: () => Promise.resolve({ enabled: true, profile: 'opus', lastError: '', queue: [], reviewers: [] }),
  roomMergeStatus: () => Promise.resolve([]),
  roomMergeHistory: () => Promise.resolve([]),
  settings: () => Promise.resolve([]),
  usageRollup: () => Promise.resolve({ byRoleModel: [{ role: 'reviewer', cost: { usd: 1.84 } }] }),
  voiceStatus: () => Promise.resolve({ available: true, live: false }),
  setReviewEnabled: (room, on) => { globalThis.recordCall(['review', room, on]); return Promise.resolve() },
}
export const openExternal = () => {}`
const code = await bundle({ component: '../services/Services', api, format })

async function mountPage() {
  const dom = installDom()
  const { dir, default: C, mount, unmount, flushSync } = await importBundle(code, '../services/Services')
  const app = mount(C, { target: document.getElementById('app'), props: { roomId: 'room', members, changes, onopen: () => {} } })
  const root = document.getElementById('app')
  assert.ok(root)
  const settle = async () => { await new Promise((r) => setTimeout(r, 0)); flushSync() }
  const done = async () => { unmount(app); dom.window.close(); for (const n of domNames) delete (globalThis as Record<string, unknown>)[n]; await rm(dir, { recursive: true, force: true }) }
  return { root, settle, done }
}

test('every service has a row with its profile and cost, and no developer call list', async () => {
  const page = await mountPage()
  try {
    assert.equal(page.root.querySelectorAll('.svc').length, 7, 'every card shows at once')
    assert.match(page.root.querySelector('.svc')?.textContent ?? '', /loading/i, 'and says loading, not unreadable, before its data arrives')
    await page.settle()
    const names = [...page.root.querySelectorAll('.svc .nm')].map((n) => n.firstChild?.textContent)
    assert.deepEqual(names, ['PR reviewers', 'Merge queue and trials', 'Conflict radar', 'Goal keeper', 'Librarian', 'Maintenance and retention', 'Voice service'])
    const reviewer = page.root.querySelector('.svc')
    assert.match(reviewer?.textContent ?? '', /opus/)
    assert.match(reviewer?.textContent ?? '', /\$1\.84/)
    assert.doesNotMatch(page.root.textContent ?? '', /Calls:/)
    assert.equal(page.root.querySelector('.svc .dot')?.classList.contains('on'), true)
  } finally { await page.done() }
})

test('opening a service shows its control, and the toggle calls the existing engine call', async () => {
  const page = await mountPage()
  try {
    await page.settle()
    const head = page.root.querySelector<HTMLButtonElement>('.svc .head')
    head?.click()
    await page.settle()
    const toggle = page.root.querySelector<HTMLInputElement>('.ctl input[type="checkbox"]')
    assert.ok(toggle, 'the reviewer control is shown')
    toggle.checked = false
    toggle.dispatchEvent(new window.Event('change', { bubbles: true }))
    await page.settle()
    assert.deepEqual(calls.filter((c) => c[0] === 'review'), [['review', 'room', false]])
  } finally { await page.done() }
})

test('the service filter hides events of other services', async () => {
  const page = await mountPage()
  try {
    await page.settle()
    const texts = () => [...page.root.querySelectorAll('.events .ev')].map((e) => e.textContent ?? '')
    assert.ok(texts().some((t) => t.includes('gave a verdict')))
    const select = page.root.querySelector<HTMLSelectElement>('.filters select')
    assert.ok(select)
    select.value = 'keeper'
    select.dispatchEvent(new window.Event('change', { bubbles: true }))
    await page.settle()
    assert.ok(!texts().some((t) => t.includes('gave a verdict')))
    assert.ok(texts().some((t) => t.includes('keeper')))
  } finally { await page.done() }
})

test('the page loads its data once, not in a loop', async () => {
  const before = calls.filter((c) => c[0] === 'listServices').length
  const page = await mountPage()
  try {
    await page.settle()
    await page.settle()
    assert.equal(calls.filter((c) => c[0] === 'listServices').length - before, 1)
  } finally { await page.done() }
})
