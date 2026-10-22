import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { setImmediate } from 'node:timers/promises'
import { bundle, domNames, FORMAT, importBundle, installDom } from '../prs/prDetailHarness.ts'
import { DEFAULT_CI_BUDGET, DEFAULT_POLICY } from '../prs/pr.ts'

type Policy = Omit<typeof DEFAULT_POLICY, 'requiredChecks'> & typeof DEFAULT_CI_BUDGET & {
  requiredChecks: string[]
  ciTestAllowNotes: Record<string, string>
}
type Harness = {
  dir: string
  default: unknown
  mount: (component: unknown, options: { target: Element; props: object }) => object
  unmount: (instance: object) => void | Promise<void>
  flushSync: (action?: () => void) => void
}
const compiled = await bundle({
  component: '../rooms/RoomSettings',
  api: 'export const api = globalThis.roomPolicyRefreshAPI',
  format: FORMAT + '; export const roomLabel = (room) => room.name; export const duration = () => "now";',
})
const policy = (next: Partial<Policy> = {}): Policy => ({ ...DEFAULT_POLICY, ...DEFAULT_CI_BUDGET, requiredChecks: ['base'], ciTestAllowNotes: {}, ...next })
function deferred<T>() {
  let resolveValue: (value: T) => void = () => { throw new Error('promise is not ready') }
  let rejectValue: (reason: unknown) => void = () => { throw new Error('promise is not ready') }
  const promise = new Promise<T>((resolve, reject) => { resolveValue = resolve; rejectValue = reject })
  return { promise, resolve: (value: T) => resolveValue(value), reject: (reason: unknown) => rejectValue(reason) }
}
function observed<T>(target: Node, read: () => T | undefined): Promise<T> {
  const current = read()
  if (current !== undefined) return Promise.resolve(current)
  return new Promise((resolve, reject) => {
    const observer = new MutationObserver(() => {
      const value = read()
      if (value === undefined) return
      clearTimeout(guard)
      observer.disconnect()
      resolve(value)
    })
    const guard = setTimeout(() => {
      observer.disconnect()
      reject(new Error('room policy controls did not reach the expected state'))
    }, 30_000)
    observer.observe(target, { childList: true, subtree: true, attributes: true })
  })
}
function guarded<T>(promise: Promise<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const guard = setTimeout(() => reject(new Error('policy save did not finish')), 30_000)
    promise.then((value) => { clearTimeout(guard); resolve(value) }, (error: unknown) => { clearTimeout(guard); reject(error) })
  })
}
type View = {
  target: HTMLElement
  saved: Promise<Policy>
  input: (selector: string) => HTMLInputElement
  click: (name: string) => void
  edit: (input: HTMLInputElement, value: string, commit?: boolean) => void
  flush: () => void
}
async function withSettings(read: () => Promise<Policy>, run: (view: View) => Promise<void>, write?: (next: Policy) => Promise<void>) {
  const saved = deferred<Policy>()
  const dom = installDom({
    roomPolicyRefreshAPI: {
      roomChangePolicy: read,
      prSettings: async () => ({}),
      setRoomChangePolicy: async (_room: string, next: Policy) => {
        saved.resolve(structuredClone(next))
        if (write) await write(next)
      },
    },
    matchMedia: () => ({ matches: true }),
  })
  const built: Harness = await importBundle(compiled, 'RoomSettings')
  const target = document.getElementById('app')
  assert.ok(target)
  const input = (selector: string) => {
    const node = target.querySelector<HTMLInputElement>(selector)
    assert.ok(node, selector)
    return node
  }
  let instance: object | undefined
  try {
    built.flushSync(() => {
      instance = built.mount(built.default, { target, props: {
        room: { id: 'r', name: 'work' }, start: 'review', reviewer: { enabled: true }, onclose() {},
      } })
    })
    await observed(target, () => target.querySelector<HTMLInputElement>('input[aria-label="Required checks"]') ?? undefined)
    await run({
      target, saved: saved.promise, input,
      click: (name) => {
        const button = [...target.querySelectorAll('button')].find((node) => node.textContent?.trim() === name)
        assert.ok(button, name)
        built.flushSync(() => button.click())
      },
      edit: (node, value, commit = false) => built.flushSync(() => {
        node.value = value
        node.dispatchEvent(new Event('input', { bubbles: true }))
        if (commit) node.dispatchEvent(new Event('change', { bubbles: true }))
      }),
      flush: () => built.flushSync(),
    })
  } finally {
    if (instance) await built.unmount(instance)
    dom.window.close()
    for (const name of [...domNames, 'roomPolicyRefreshAPI', 'matchMedia']) Reflect.deleteProperty(globalThis, name)
    await rm(built.dir, { recursive: true, force: true })
  }
}

test('a rapid edit survives the pending review refresh and reaches the eventual save', async () => {
  const reply = deferred<Policy>()
  let reads = 0
  await withSettings(async () => ++reads === 1 ? policy() : reply.promise, async (view) => {
    view.click('CI time')
    const warning = view.input('#rp-ci-warn')
    view.edit(warning, '1m')
    reply.resolve(policy({ review: 'off', ciFailAfter: '4m' }))
    await setImmediate()
    view.flush()
    assert.equal(view.input('#rp-ci-fail').value, '4m', 'the pending response has reached the form')
    assert.equal(warning.value, '1m', 'the refresh must preserve the edit made during its read')
    view.edit(warning, '1m', true)
    const saved = await guarded(view.saved)
    assert.equal(saved.ciWarnAfter, '1m')
    assert.equal(saved.ciFailAfter, '4m')
    assert.equal(saved.review, 'off', 'an unchanged review choice adopts the toggle refresh')
  })
})

test('an unchanged form adopts the refreshed review policy', async () => {
  const reply = deferred<Policy>()
  let reads = 0
  await withSettings(async () => ++reads === 1 ? policy() : reply.promise, async (view) => {
    view.click('CI time')
    reply.resolve(policy({ review: 'off', ciWarnAfter: '1m', ciFailAfter: '4m', requiredChecks: ['latest'] }))
    await setImmediate()
    view.flush()
    assert.equal(view.input('#rp-ci-fail').value, '4m', 'the pending response has reached the form')
    const warning = view.input('#rp-ci-warn')
    assert.equal(warning.value, '1m')
    view.edit(warning, '30s', true)
    const saved = await guarded(view.saved)
    assert.equal(saved.review, 'off')
    assert.deepEqual(saved.requiredChecks, ['latest'])
  })
})

test('an older policy response cannot replace the newer response', async () => {
  const older = deferred<Policy>()
  const newer = deferred<Policy>()
  let reads = 0
  await withSettings(async () => ++reads === 1 ? policy() : reads === 2 ? older.promise : newer.promise, async (view) => {
    view.click('CI time')
    view.click('Review & landing')
    newer.resolve(policy({ requiredChecks: ['newer'] }))
    await setImmediate()
    view.flush()
    assert.equal(view.input('input[aria-label="Required checks"]').value, 'newer')
    older.resolve(policy({ requiredChecks: ['older'] }))
    await setImmediate()
    view.flush()
    assert.equal(view.input('input[aria-label="Required checks"]').value, 'newer')
  })
})

test('an edit committed during the refresh saves the refreshed review choice', async () => {
  const reply = deferred<Policy>()
  let reads = 0
  await withSettings(async () => ++reads === 1 ? policy() : reply.promise, async (view) => {
    view.click('CI time')
    view.edit(view.input('#rp-ci-warn'), '1m', true)
    reply.resolve(policy({ review: 'off', ciFailAfter: '4m' }))
    const saved = await guarded(view.saved)
    assert.equal(saved.ciWarnAfter, '1m')
    assert.equal(saved.review, 'off', 'committing the edit must not restore the review choice from before the refresh')
    assert.equal(saved.ciFailAfter, '4m')
  })
})


test('a failed refresh preserves the draft and retries only after a new user action', async () => {
  const firstFailure = deferred<Policy>()
  const secondFailure = deferred<Policy>()
  const recovery = deferred<Policy>()
  let reads = 0
  await withSettings(async () => {
    reads += 1
    if (reads === 1) return policy()
    if (reads === 2) return firstFailure.promise
    if (reads === 3) return secondFailure.promise
    if (reads === 4) return recovery.promise
    throw new Error('unexpected automatic policy retry')
  }, async (view) => {
    view.click('CI time')
    const warning = view.input('#rp-ci-warn')
    view.edit(warning, '1m', true)
    firstFailure.reject(new Error('read failed'))
    await setImmediate()
    view.flush()
    assert.equal(warning.value, '1m')
    assert.equal(reads, 2, 'a failed read must not retry itself')
    view.edit(warning, '45s', true)
    assert.equal(reads, 3, 'a new commit must request fresh policy before saving')
    secondFailure.reject(new Error('read failed again'))
    await setImmediate()
    view.flush()
    assert.equal(warning.value, '45s')
    assert.equal(reads, 3, 'a second failed read still waits for the user')
    view.click('Retry save')
    assert.equal(reads, 4, 'the explicit retry uses the same fresh-read path')
    recovery.resolve(policy({ review: 'off', ciFailAfter: '4m' }))
    const saved = await guarded(view.saved)
    assert.equal(saved.ciWarnAfter, '45s')
    assert.equal(saved.review, 'off')
    assert.equal(saved.ciFailAfter, '4m')
  })
})

test('a save that invalidates a pending refresh leaves the next field commit writable', async () => {
  const firstWrite = deferred<void>()
  const pendingRead = deferred<Policy>()
  const writes: Policy[] = []
  let reads = 0
  await withSettings(async () => ++reads === 1 ? policy() : pendingRead.promise, async (view) => {
    view.edit(view.input('input[aria-label="Required checks"]'), 'first', true)
    assert.equal(writes.length, 1, 'the first write is pending')
    view.click('CI time')
    assert.equal(reads, 2, 'pane navigation starts a read while the write is pending')
    firstWrite.resolve(undefined)
    await setImmediate()
    view.flush()
    const warning = view.input('#rp-ci-warn')
    assert.equal(warning.disabled, false, 'the first write has completed')
    pendingRead.resolve(policy({ requiredChecks: ['older'] }))
    await setImmediate()
    view.flush()
    view.edit(warning, '1m', true)
    assert.equal(writes.length, 2, 'a second field commit must reach the API after the invalidated read settles')
    assert.equal(writes[1]?.ciWarnAfter, '1m')
    assert.deepEqual(writes[1]?.requiredChecks, ['first'])
  }, async (next) => {
    writes.push(structuredClone(next))
    if (writes.length === 1) await firstWrite.promise
  })
})
