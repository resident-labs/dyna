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
  globalHookRuns: () => Promise.resolve([{ id: 'r1', result: 'error', startedAt: new Date().toISOString(), finishedAt: new Date().toISOString() }]),
  globalHookRunLog: () => Promise.reject(new Error('log gone')),
  hookRuns: () => Promise.resolve([]),
  listHooks: () => Promise.resolve([{ name: 'self-update', label: 'Update Dyna', event: 'main-updated', command: 'x' }, { name: 'lint', event: 'schedule', everyMinutes: 60, command: 'make lint' }]),
  hookSummary: () => Promise.resolve([]),
  listGlobalHooks: () => Promise.resolve([]),
  globalHookSummary: () => Promise.resolve([]),
  runService: (name) => { record('run ' + name); return Promise.resolve({ result: 'pass' }) },
  runHook: (repo, name) => { record('hook ' + repo + ' ' + name); return Promise.resolve({ result: 'pass' }) },
  hookRunLog: () => Promise.resolve(null),
}
export const openExternal = () => {}
`

test('the page shows the registry groups in order, Not set up last, then Hooks without service hooks; only the failed service has a pill', async () => {
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
    assert.deepEqual(titles, ['Pull requests', 'Agents', 'This computer', 'Not set up', 'Hooks'])
    const rowOf = (label: string): HTMLElement => need([...root.querySelectorAll<HTMLElement>('.setting')].find((r) => r.querySelector('h3')?.textContent === label), label)
    // r16: one danger pill for the failed run; a healthy row says no "On".
    const pills = [...root.querySelectorAll('.status.pill')]
    assert.deepEqual(pills.map((p) => [p.textContent, p.classList.contains('danger')]), [['Last run failed', true]])
    assert.ok(rowOf('updates').querySelector('.status.pill.danger'))
    assert.match(rowOf('library').textContent ?? '', /Idle · glm-flash/)
    assert.doesNotMatch(rowOf('maintenance').querySelector('.status')?.textContent ?? '', /\bOn\b/)
    // The Update Dyna hook does the Updates service's work, so it shows only there.
    const hookRows = [...need(root.querySelector('#hooks')).querySelectorAll('.setting h3')].map((h) => h.textContent)
    assert.deepEqual(hookRows, ['lint'])
    // A click on the row opens its detail in place, where Run now runs the pass.
    need(rowOf('maintenance').querySelector<HTMLElement>('button.toggle')).click()
    await settle()
    // A failed run whose log cannot be read still shows.
    assert.match(rowOf('maintenance').textContent ?? '', /maintenance · failed/)
    const run = need([...rowOf('maintenance').querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === 'Run now'))
    run.click()
    await settle()
    assert.deepEqual(globalThis.__writes, ['run maintenance'])
    // Updates runs its repository hook.
    need(rowOf('updates').querySelector<HTMLElement>('button.toggle')).click()
    await settle()
    need([...rowOf('updates').querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === 'Run now')).click()
    await settle()
    assert.deepEqual(globalThis.__writes, ['run maintenance', 'hook /repo self-update'])
  } finally {
    unmount(need(app))
    dom.window.close()
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
  }
})

test('a refused setting keeps the typed text so the owner can correct it', async () => {
  const dom = installDom()
  const { default: C, mount, unmount, flushSync }: Harness = await importBundle(await bundle({ component: '../services/SettingControl', api, format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8') }), '../services/SettingControl')
  const root = need(document.getElementById('app'))
  let app: Mounted | undefined
  const item = { key: 'runs.retention', value: '336h0m0s', display: '14d', kind: 'duration', source: 'file', liveVsRestart: '', help: '' }
  flushSync(() => { app = mount(C, { target: root, props: { item, label: 'Keep finished runs', onsave: () => Promise.reject(new Error('bad')) } }) })
  try {
    const input = need(root.querySelector('input'))
    input.value = '2 weeks'
    input.dispatchEvent(new window.Event('input'))
    input.dispatchEvent(new window.Event('blur'))
    for (let i = 0; i < 3; i++) { await new Promise((r) => setTimeout(r, 0)); flushSync() }
    assert.equal(input.value, '2 weeks')
  } finally {
    unmount(need(app))
    dom.window.close()
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
  }
})

test('an edit typed while a save is pending survives that save', async () => {
  const dom = installDom()
  const { default: C, mount, unmount, flushSync }: Harness = await importBundle(await bundle({ component: '../services/SettingControl', api, format: readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8') }), '../services/SettingControl')
  const root = need(document.getElementById('app'))
  let app: Mounted | undefined
  let done = (): void => {}
  const item = { key: 'worktrees.root', value: '~/a', kind: 'string', source: 'file', liveVsRestart: '', help: '' }
  flushSync(() => { app = mount(C, { target: root, props: { item, label: 'Folder for new worktrees', onsave: () => new Promise<void>((r) => { done = r }) } }) })
  try {
    const input = need(root.querySelector('input'))
    const type = (text: string): void => { input.value = text; input.dispatchEvent(new window.Event('input')) }
    type('~/b')
    input.dispatchEvent(new window.Event('blur'))
    type('~/c')
    done()
    for (let i = 0; i < 3; i++) { await new Promise((r) => setTimeout(r, 0)); flushSync() }
    assert.equal(input.value, '~/c')
  } finally {
    unmount(need(app))
    dom.window.close()
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
  }
})
