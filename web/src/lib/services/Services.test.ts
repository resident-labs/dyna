import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'

type Mounted = object
// The harness is plain JavaScript, so its exports are typed here at the boundary.
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => Mounted
  unmount: (app: Mounted) => void
  flushSync: (fn?: () => void) => void
}

// The fixture answers every call the page makes. A write records itself on the
// shared global, because the page runs as its own module instance.
declare global { var __writes: string[] }
const api = `
const row = (id, group, extra) => ({ id, group, label: id, description: 'Does ' + id, state: 'on', detail: '', count: 0, profile: '', session: '', settingsKeys: [], ...extra })
const record = (w) => { globalThis.__writes = (globalThis.__writes || []).concat(w) }
export const api = {
  serviceRegistry: () => Promise.resolve([
    row('reviewer', 'pull-requests', { switch: { on: true }, profile: 'terra' }),
    row('lane-sync', 'pull-requests', { state: 'not-set-up', settingsKeys: ['lane.local.repos'] }),
    row('library', 'agents', { state: 'idle', switch: { on: true }, profile: 'glm-flash' }),
    row('updates', 'this-computer', { state: 'error', detail: 'last-run-failed', switch: { on: true }, hook: 'self-update' }),
    row('maintenance', 'this-computer', { switch: { on: true }, schedule: { everyMinutes: 60, timeout: '10m0s' } }),
  ]),
  settings: () => Promise.resolve([]),
  globalHookRuns: () => Promise.resolve([]),
  hookRuns: () => Promise.resolve([]),
  runService: (name) => { record('run ' + name); return Promise.resolve({ result: 'pass' }) },
}
export const openExternal = () => {}
`

test('the page shows the registry groups in order with Not set up last; only the failed service has a pill; Run now runs the pass', async () => {
  globalThis.__writes = []
  const dom = installDom()
  const { default: C, mount, unmount, flushSync }: Harness = await importBundle(await bundle({ component: '../services/Services', api, format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8') }), '../services/Services')
  const root = need(document.getElementById('app'))
  let app: Mounted | undefined
  flushSync(() => { app = mount(C, { target: root, props: { roomId: 'room_1', repo: '/repo' } }) })
  const settle = async () => { for (let i = 0; i < 6; i++) { await new Promise((r) => setTimeout(r, 0)); flushSync() } }
  try {
    await settle()
    const titles = [...root.querySelectorAll('section.section > header h2')].map((h) => h.textContent)
    assert.deepEqual(titles, ['Pull requests', 'Agents', 'This computer', 'Not set up'])
    const rowOf = (label: string): HTMLElement => need([...root.querySelectorAll<HTMLElement>('.setting')].find((r) => r.querySelector('h3')?.textContent === label), label)
    // r16: one danger pill for the failed run; a healthy row says no "On".
    const pills = [...root.querySelectorAll('.status.pill')]
    assert.deepEqual(pills.map((p) => [p.textContent, p.classList.contains('danger')]), [['Last run failed', true]])
    assert.ok(rowOf('updates').querySelector('.status.pill.danger'))
    assert.match(rowOf('library').textContent ?? '', /Idle · glm-flash/)
    assert.doesNotMatch(rowOf('maintenance').querySelector('.status')?.textContent ?? '', /\bOn\b/)
    // A click on the row opens its detail in place, where Run now runs the pass.
    need(rowOf('maintenance').querySelector<HTMLElement>('button.toggle')).click()
    flushSync()
    const run = need([...rowOf('maintenance').querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === 'Run now'))
    run.click()
    await settle()
    assert.deepEqual(globalThis.__writes, ['run maintenance'])
  } finally {
    unmount(need(app))
    dom.window.close()
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
  }
})
