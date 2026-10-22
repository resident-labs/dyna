import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { createTestServer } from '../../../../test/ssr-server.ts'

// The server starts outside the test clock; the test measures only the render.
const vite = await createTestServer({ after })
const { render } = await vite.ssrLoadModule('svelte/server')
const { default: Meter } = await vite.ssrLoadModule('/src/lib/settings/usage/UsageMeter.svelte')

test('usage meters show uncertain resets without a numeric meter in both layouts', () => {
  for (const compact of [false, true]) {
    const window = { label: 'Weekly', usedPercent: 99, resetsAt: '2020-01-02T00:00:00Z' }
    const html = render(Meter, { props: { window, observedAt: '2020-01-01T00:00:00Z', compact } }).body
    assert.match(html, /Weekly: probably reset · last checked /)
    assert.doesNotMatch(html, /role="meter"|99%|100%|1%/)
    const fresh = render(Meter, { props: { window, observedAt: '2020-01-03T00:00:00Z', compact } }).body
    assert.match(fresh, /1% left/)
    assert.doesNotMatch(fresh, /probably reset/)
  }
})

// Bundle outside the test clock; the regression measures only a mounted view.
const { bundle, domNames, importBundle, installDom } = await import('../../prs/prDetailHarness.ts')
const meterCode = await bundle({ component: '../settings/usage/UsageMeter' })
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => object
  unmount: (app: object) => Promise<void>
  flushSync: (fn?: () => void) => void
}

test('an open usage meter changes at its reset deadline without a new account read', async (t) => {
  const { rm } = await import('node:fs/promises')
  const dom = installDom()
  const { dir, default: Meter, mount, unmount, flushSync }: Harness = await importBundle(meterCode, 'UsageMeter')
  const target = document.getElementById('app')
  assert.ok(target)
  const now = Date.parse('2026-10-20T12:00:00Z')
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now })
  let app: object | undefined
  const clear = t.mock.method(globalThis, 'clearTimeout')
  try {
    flushSync(() => {
      app = mount(Meter, { target, props: {
        window: { label: 'Weekly', usedPercent: 99, resetsAt: new Date(now + 60000).toISOString() },
        observedAt: new Date(now - 60000).toISOString(),
      } })
    })
    assert.match(target.textContent ?? '', /1% left/)
    t.mock.timers.tick(59999)
    flushSync()
    assert.doesNotMatch(target.textContent ?? '', /probably reset/)
    t.mock.timers.tick(1)
    flushSync()
    assert.match(target.textContent ?? '', /Weekly: probably reset · last checked /)
    assert.equal(target.querySelector('[role="meter"]'), null)
    assert.ok(app)
    await unmount(app)
    app = undefined
    const cleared = clear.mock.callCount()
    flushSync(() => {
      app = mount(Meter, { target, props: {
        window: { label: 'Weekly', usedPercent: 99, resetsAt: new Date(now + 120000).toISOString() },
        observedAt: new Date(now).toISOString(),
      } })
    })
    assert.ok(app)
    await unmount(app)
    app = undefined
    assert.equal(clear.mock.callCount(), cleared + 1, 'unmount clears the pending reset deadline')
  } finally {
    if (app) await unmount(app)
    clear.mock.restore()
    t.mock.timers.reset()
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})
