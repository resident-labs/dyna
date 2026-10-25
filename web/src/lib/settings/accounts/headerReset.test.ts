import test from 'node:test'
import assert from 'node:assert/strict'

// Bundle outside the test clock; the regression measures only a mounted view.
const { bundle, domNames, importBundle, installDom } = await import('../../prs/prDetailHarness.ts')
const cardCode = await bundle({ component: '../settings/accounts/SubscriptionCard' })
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => object
  unmount: (app: object) => Promise<void>
  flushSync: (fn?: () => void) => void
}

test('an open usage card stops saying limit reached when its blocking window resets', async (t) => {
  const { rm } = await import('node:fs/promises')
  const dom = installDom()
  const { dir, default: Card, mount, unmount, flushSync }: Harness = await importBundle(cardCode, 'SubscriptionCard')
  const target = document.getElementById('app')
  assert.ok(target)
  const now = Date.parse('2026-10-20T12:00:00Z')
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now })
  let app: object | undefined
  try {
    flushSync(() => {
      app = mount(Card, { target, props: {
        group: { provider: 'chatgpt', accounts: [{
          id: 'a', harness: 'pi', provider: 'chatgpt', label: 'Work account', plan: { id: 'max', label: 'Max' }, status: 'ready', activeHere: true, active: true, observedAt: new Date(now).toISOString(),
          windows: [{ id: 'five_hour', label: 'Session', usedPercent: 100, resetsAt: new Date(now + 60000).toISOString() }],
        }] },
      } })
    })
    assert.match(target.textContent ?? '', /Session limit reached/, 'the header reports the exhausted window')
    t.mock.timers.tick(59999)
    flushSync()
    assert.match(target.textContent ?? '', /Session limit reached/, 'the limit holds until its reset')
    t.mock.timers.tick(1)
    flushSync()
    assert.doesNotMatch(target.textContent ?? '', /limit reached/, 'the header drops a limit whose window has reset, with no new account read')
    assert.match(target.textContent ?? '', /Max/, 'the card falls back to the plan')
    assert.ok(app)
  } finally {
    if (app) await unmount(app)
    t.mock.timers.reset()
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})

test('a second account still drops its limit after an earlier deadline has passed', async (t) => {
  // One card holds both accounts, so one clock serves them: an expired host
  // deadline on the first must not win over the reset the second one waits for.
  const { rm } = await import('node:fs/promises')
  const dom = installDom()
  const { dir, default: Card, mount, unmount, flushSync }: Harness = await importBundle(cardCode, 'SubscriptionCard')
  const target = document.getElementById('app')
  assert.ok(target)
  const now = Date.parse('2026-10-20T12:00:00Z')
  t.mock.timers.enable({ apis: ['setTimeout', 'Date'], now })
  let app: object | undefined
  try {
    flushSync(() => {
      app = mount(Card, { target, props: {
        group: { provider: 'chatgpt', accounts: [
          { id: 'a', harness: 'codex', label: 'Old block', status: 'ready', active: false, quotaBlocked: true, blockingQuotaWindows: ['five_hour'], quotaBlockedUntil: new Date(now - 3600000).toISOString() },
          { id: 'b', harness: 'claude-code', label: 'Weekly account', status: 'ready', active: false, windows: [{ id: 'seven_day', label: 'Weekly', usedPercent: 100, resetsAt: new Date(now + 60000).toISOString() }] },
        ] },
      } })
    })
    assert.match(target.textContent ?? '', /Weekly limit reached/, 'the exhausted window blocks')
    assert.doesNotMatch(target.textContent ?? '', /Session limit reached/, 'the expired host deadline does not block')
    t.mock.timers.tick(60000)
    flushSync()
    assert.doesNotMatch(target.textContent ?? '', /limit reached/, 'the later account drops its limit at its own reset')
    assert.ok(app)
  } finally {
    if (app) await unmount(app)
    t.mock.timers.reset()
    dom.window.close()
    for (const name of domNames) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})
