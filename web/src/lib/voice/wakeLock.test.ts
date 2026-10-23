import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createScreenLock, type WakeDocument } from './wakeLock.ts'

// settled waits for the lock's pending request to finish, without a timer.
const settled = () => new Promise<void>((done) => setImmediate(done))

// fakePage is the page the lock reads, driven by the test.
function fakePage() {
  const listeners = new Set<() => void>()
  let state = 'visible'
  const page: WakeDocument = {
    get visibilityState() { return state },
    addEventListener: (_type, fn) => { listeners.add(fn) },
    removeEventListener: (_type, fn) => { listeners.delete(fn) },
  }
  const tell = () => { for (const fn of Array.from(listeners)) fn() }
  const show = () => { state = 'visible'; tell() }
  const hide = () => { state = 'hidden'; tell() }
  return { page, show, hide }
}

// fakeWakeLock is the browser's wake lock: it counts requests and releases,
// and can refuse every request as an insecure page or a low battery does.
function fakeWakeLock(refuse = false) {
  const lock = {
    asked: 0, // requests
    live: 0, // locks the page holds
    released: 0, // releases
    api: {
      request: async (_type: 'screen') => {
        lock.asked++
        if (refuse) throw new Error('not allowed')
        lock.live++
        return { release: async () => { lock.live--; lock.released++ } }
      },
    },
  }
  return lock
}

test('a call keeps the screen on, and takes the lock again when the page returns', async () => {
  const page = fakePage()
  const lock = fakeWakeLock()
  const screen = createScreenLock({ wakeLock: lock.api, doc: page.page })

  await screen.keep(true)
  assert.deepEqual([lock.asked, lock.live], [1, 1])

  // A later state of the same call asks for nothing more.
  await screen.keep(true)
  assert.equal(lock.asked, 1)

  // The browser drops the lock when the page hides: the page's return takes it
  // again.
  page.hide()
  assert.equal(lock.live, 0)
  page.show()
  await settled()
  assert.deepEqual([lock.asked, lock.live], [2, 1])

  // The end of the call lets it go, and a hidden page asks for nothing.
  await screen.keep(false)
  assert.equal(lock.live, 0)
  page.hide()
  page.show()
  await settled()
  assert.deepEqual([lock.asked, lock.live], [2, 0])
})

test('a browser without the API, or that refuses the lock, leaves the call as it is', async () => {
  const page = fakePage()
  const refusing = createScreenLock({ wakeLock: fakeWakeLock(true).api, doc: page.page })
  await refusing.keep(true) // the refusal does not throw
  page.hide()
  page.show()
  await settled()
  await refusing.keep(false)

  // No API: nothing is asked, and the lock does nothing.
  const missing = createScreenLock({ wakeLock: null, doc: page.page })
  await missing.keep(true)
  page.hide()
  page.show()
  await settled()
  await missing.keep(false)
})
