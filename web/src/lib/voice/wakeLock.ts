// The Screen Wake Lock keeps a phone's screen on during a Voice call: a phone
// dims and locks itself when it sees no touch, and a call has none. A browser
// without the API, or one that refuses the lock (no secure context, a low
// battery), leaves the call as it is: this module tells the owner nothing.

// WakeSentinel is a granted screen wake lock, as the call drives it.
export interface WakeSentinel {
  release(): Promise<void>
}

// WakeLockApi is the part of navigator.wakeLock the call uses.
export interface WakeLockApi {
  request(type: 'screen'): Promise<WakeSentinel>
}

// WakeDocument is the part of the page the lock reads: whether it is visible,
// and the event that says it changed.
export interface WakeDocument {
  readonly visibilityState: string
  addEventListener(type: 'visibilitychange', fn: () => void): void
  removeEventListener(type: 'visibilitychange', fn: () => void): void
}

// WakeDeps are what the lock runs on: the browser's wake lock and the page
// (tests drive them).
export interface WakeDeps {
  wakeLock?: WakeLockApi | null
  doc?: WakeDocument
}

// ScreenLock is the call's hold on the screen.
export interface ScreenLock {
  // keep says whether a call wants the screen awake. It takes the lock while
  // the page is visible, and lets it go otherwise.
  keep(on: boolean): Promise<void>
  stop(): void
}

// browserWakeLock is the page's wake lock, or null where the browser has none.
const browserWakeLock = (): WakeLockApi | null => (typeof navigator === 'undefined' ? null : navigator.wakeLock ?? null)

// browserDocument is the page, or null where there is none (a test with no DOM).
const browserDocument = (): WakeDocument | null => (typeof document === 'undefined' ? null : document)

export function createScreenLock(deps: WakeDeps = {}): ScreenLock {
  const api = deps.wakeLock === undefined ? browserWakeLock() : deps.wakeLock
  const doc = deps.doc ?? browserDocument()
  let wanted = false // a call runs
  let sentinel: WakeSentinel | null = null // the granted lock, while the page holds it
  let asking = false // a request is in flight
  let stopped = false

  // drop lets the lock go, so the browser may dim the screen again.
  function drop() {
    const old = sentinel
    sentinel = null
    // A refused or already released lock must not stop the call: a rejected
    // release changes nothing.
    void old?.release().catch(() => {})
  }

  // take asks for the lock while a call wants it and the page is visible.
  async function take(): Promise<void> {
    if (!api || stopped || !wanted || sentinel || asking) return
    if (doc && doc.visibilityState !== 'visible') return
    asking = true
    try {
      const got = await api.request('screen')
      // The call ended, or the page hid, while the browser answered.
      if (stopped || !wanted || (doc && doc.visibilityState !== 'visible')) {
        void got.release().catch(() => {})
        return
      }
      sentinel = got
    } catch {
      // The browser refused the lock: the call goes on without it, and the
      // next look at a visible page asks again.
    } finally {
      asking = false
    }
  }

  // The browser drops the lock when the page hides, so the page's return asks
  // for it again.
  const onVisibility = () => {
    if (!doc) return
    if (doc.visibilityState === 'visible') void take()
    else drop()
  }
  doc?.addEventListener('visibilitychange', onVisibility)

  return {
    keep(on: boolean): Promise<void> {
      if (on === wanted || stopped) return Promise.resolve()
      wanted = on
      if (!on) {
        drop()
        return Promise.resolve()
      }
      return take()
    },
    stop() {
      if (stopped) return
      stopped = true
      wanted = false
      doc?.removeEventListener('visibilitychange', onVisibility)
      drop()
    },
  }
}
