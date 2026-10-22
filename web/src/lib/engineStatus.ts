// engineWatch turns the event stream's state into one connection status for
// the banner. While the stream is down the status is "reconnecting". After
// graceMs it also says why, so an engine restart does not flash an alarm:
//
//   offline      the browser says this device has no network.
//   unreachable  no answer came, and the page is served from another
//                computer, so this device's network may be the problem; or
//                a server that is not the engine answered with a 4xx.
//   engine       the engine, or the proxy in front of it, answered with an
//                error; or no answer came on this computer, where only the
//                engine can be at fault. Only this status names engine commands.
//
// The reason is checked again every graceMs until the stream is back. A 401
// stops the checks: this device is signed out, and the pairing screen takes
// over.

export type EngineStatus = '' | 'reconnecting' | 'offline' | 'unreachable' | 'engine'

// EngineIO is how the watch looks at the browser. Tests replace it.
export interface EngineIO {
  online: () => boolean
  // check returns the engine's HTTP status, or 'network' when no answer came.
  check: () => Promise<number | 'network'>
  // local is true when the page is served from this computer.
  local: boolean
  // listen runs fn when the browser goes online or offline. It returns a
  // function that stops listening.
  listen: (fn: () => void) => () => void
}

// diagnose says why the stream is down: a reason, '' when the engine
// answers, or null when this device is signed out.
export async function diagnose(io: EngineIO): Promise<Exclude<EngineStatus, 'reconnecting'> | null> {
  if (!io.online()) return 'offline'
  const status = await io.check()
  if (status === 'network') return io.local ? 'engine' : 'unreachable'
  if (status === 401) return null
  if (status >= 500) return 'engine'
  return status >= 400 ? 'unreachable' : ''
}

// engineWatch calls onChange only when the status changes. state(up) takes
// onEvent's stream state.
export function engineWatch(onChange: (s: EngineStatus) => void, io: EngineIO, graceMs = 5000) {
  let timer: ReturnType<typeof setTimeout> | undefined
  let status: EngineStatus = ''
  let up = true
  // turn makes a late diagnosis from an earlier loss harmless.
  let turn = 0
  const set = (s: EngineStatus) => {
    if (status === s) return
    status = s
    onChange(s)
  }
  const check = async () => {
    const mine = turn
    const why = await diagnose(io)
    if (up || mine !== turn) return
    set(why || 'reconnecting')
    if (why !== null) timer = setTimeout(check, graceMs)
  }
  const lost = () => {
    turn++
    clearTimeout(timer)
    if (!io.online()) set('offline')
    else if (status !== 'reconnecting') set('reconnecting')
    timer = setTimeout(check, graceMs)
  }
  // Only the stream says that Dyna is cut off. An offline event while the
  // stream still works changes nothing, so a wrong onLine raises no alarm;
  // a dead stream is noticed within 30 seconds.
  const unlisten = io.listen(() => {
    if (!up) lost()
  })
  return {
    state(next: boolean) {
      if (next === up) return
      up = next
      if (up) {
        turn++
        clearTimeout(timer)
        set('')
      } else lost()
    },
    stop() {
      turn++
      clearTimeout(timer)
      unlisten()
    },
  }
}

// browserEngine is the EngineIO for the running app.
export function browserEngine(check: EngineIO['check']): EngineIO {
  return {
    online: () => navigator.onLine,
    check,
    local: ['localhost', '127.0.0.1', '[::1]'].includes(location.hostname) || location.hostname.endsWith('.localhost'),
    listen: (fn) => {
      window.addEventListener('online', fn)
      window.addEventListener('offline', fn)
      return () => {
        window.removeEventListener('online', fn)
        window.removeEventListener('offline', fn)
      }
    },
  }
}
