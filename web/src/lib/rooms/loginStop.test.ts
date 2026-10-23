import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { parse } from 'svelte/compiler'
import vm from 'node:vm'
import { stripTypes } from '../../../test/svelte-source.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const ast = parse(source, { modern: true })
assert.ok(ast.instance)
const mount = ast.instance.content.body.find((node) =>
  node.type === 'ExpressionStatement' && node.expression.type === 'CallExpression' &&
  node.expression.callee.type === 'Identifier' && node.expression.callee.name === 'onMount')
assert.ok(mount && mount.type === 'ExpressionStatement' && mount.expression.type === 'CallExpression')
const callback = mount.expression.arguments[0]
assert.ok(callback && callback.type === 'ArrowFunctionExpression')
assert.ok('start' in callback && typeof callback.start === 'number' && 'end' in callback && typeof callback.end === 'number')
const mountSource = source.slice(callback.start, callback.end)

// Run the real mount callback with event and timer controls. The tests cover
// a running app and a 401 that arrives before boot has finished.
function mounted() {
  const intervals = new Map<number, () => void>()
  const listeners = new Map<string, () => void>()
  const destroyed: string[] = []
  let nextTimer = 0, starts = 0, stops = 0
  let unauthorized = () => {}
  let finishBoot: (value: null) => void = () => {}
  const boot = new Promise<null>((resolve) => { finishBoot = resolve })
  const events = {
    addEventListener: (name: string, fn: () => void) => listeners.set(name, fn),
    removeEventListener: (name: string) => listeners.delete(name),
  }
  const controller = (name: string) => ({ destroy: () => destroyed.push(name) })
  const bulkControllers = new Map<string, { destroy(): void }>()
  const state = {
    window: events,
    document: { ...events, visibilityState: 'visible' },
    sessionTab: 'chat', pageVisible: true, started: true, needPair: false,
    roomId: 'room-1', drill: null, view: 'computers', syncRepo: null,
    syncDelay: () => 0, loadSync: () => {}, loadComputers: () => {}, refreshVisibleRoom: () => {},
    setInterval: (fn: () => void) => { const id = ++nextTimer; intervals.set(id, fn); return id },
    clearInterval: (id: number) => intervals.delete(id),
    boot: () => boot,
    start: () => { starts++; return () => { stops++ } },
    // A hook card's window event: this test only checks that it is wired and
    // released with the rest.
    HOOK_LOG_EVENT: 'dyna:hook-log', hookLogFrom: () => null, openHookLog: () => {},
    onUnauthorized: (fn: () => void) => { unauthorized = fn },
    bulkControllers,
    // The selection module drops the batch controllers on stop.
    selectionView: { destroy: () => { for (const entry of bulkControllers.values()) entry.destroy() } },
    roomSessionController: controller('controls'),
    roomControlsVisit: 0, consolidationAlive: true,
    roomData: controller('rooms'), workflowData: controller('workflow'),
    // The open transcript's controller lives in lib/app/sessionView.svelte.ts now.
    transcripts: { start() {}, schedule() {}, destroy: () => destroyed.push('session') },
  }
  const unmount: () => void = vm.runInNewContext(stripTypes(`(${mountSource})()`), state)
  return {
    state, intervals, listeners, destroyed, unmount,
    unauthorized: () => unauthorized(),
    finishBoot: async () => { finishBoot(null); await boot },
    starts: () => starts, stops: () => stops,
  }
}

test('a 401 opens login and stops event work, polling and data controllers', async () => {
  const app = mounted()
  await app.finishBoot()
  assert.equal(app.starts(), 1)
  assert.ok(app.intervals.size > 0)
  app.unauthorized()
  assert.equal(app.state.needPair, true)
  assert.equal(app.stops(), 1)
  assert.equal(app.intervals.size, 0)
  assert.equal(app.listeners.size, 0)
  assert.deepEqual(app.destroyed, ['controls', 'rooms', 'session', 'workflow'])
  app.unauthorized()
  app.unmount()
  assert.equal(app.stops(), 1, 'cleanup runs once')
})

test('a 401 during boot cannot start the app after login opens', async () => {
  const app = mounted()
  app.unauthorized()
  await app.finishBoot()
  assert.equal(app.state.needPair, true)
  assert.equal(app.starts(), 0)
  assert.equal(app.intervals.size, 0)
})
