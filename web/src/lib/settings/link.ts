// The Resident Labs Connect flows in the browser: sign-in through the relay,
// the landing page, session renewal, route choice and Web Push. The helpers
// are in connect.ts; the calls are in api.js. All of this runs only in the
// web app, and only when the machine is linked.
import { connect, features, connectSession, connectHandoff, connectRoutes, whoami, renew, onRenewed, pushStatus, pushSubscribe, pushNames, pushUnsubscribe, pushTest } from '../../api.js'
import { b64uDecode, randomB64u, deviceKey, publicJwk, thumbprint, getPin, putPin, rank, ROUTE_LABEL, allowedRoute, verifyHello } from './connect.ts'

const STATE_KEY = 'dyna-connect-state'

// This machine's Connect link, as the server's page names it.
interface ConnectConfig {
  linked: boolean
  machine: string
  relay: string
  key: string
}
// api.js reads the config from the page, so its type is loose; this is the shape the page writes.
const link: ConnectConfig | null = connect
// A route to this machine: its address and how it is reached.
interface Route {
  app: string
  kind: string
}
// What the sidebar shows about the route in use.
export interface RouteNotice {
  label: string
  kind: string
  better: string
}
export interface BootResult {
  key?: true
  error?: string
}


// signIn sends the browser to the relay. The relay sends it back to
// /connect on this route with a one-time code in the URL fragment.
export async function signIn(): Promise<void> {
  const jkt = await thumbprint(await publicJwk(await deviceKey()))
  const state = randomB64u(16)
  sessionStorage.setItem(STATE_KEY, state)
  if (!link) return
  const q = new URLSearchParams({ machine: link.machine, origin: location.origin, jkt, state })
  location.href = `${link.relay}/open?${q}`
}

// pinnedKey is the machine key we trust: the pin if there is one, else the
// key the server's page names.
async function pinnedKey(): Promise<string> {
  return (link && (await getPin(link.machine))?.key) ?? link?.key ?? ''
}

// boot runs before the app starts. It returns null to go on, or
// {key: true} for the hard stop on a key mismatch, or {error} when the
// landing page could not open a session.
export async function boot(): Promise<BootResult | null> {
  if (!link?.linked) return null
  const pin = await getPin(link.machine).catch(() => null)
  if (pin && pin.key !== link.key) return { key: true }
  if (location.pathname !== '/connect') return null

  const f = new URLSearchParams(location.hash.slice(1))
  const handoff = f.get('handoff'), code = f.get('code')
  const secret = handoff ? { handoff } : { code: code ?? '' }
  const saved = sessionStorage.getItem(STATE_KEY)
  const bad = !(handoff || code) || (!handoff && code && (!saved || f.get('state') !== saved))
  if (bad) return { error: 'This sign-in link does not work.' }
  if (f.get('mid') !== link.machine || f.get('mk') !== link.key) return { key: true }
  sessionStorage.removeItem(STATE_KEY)
  history.replaceState(null, '', '/')
  try {
    await putPin(link.machine, link.key)
    await connectSession(secret)
  } catch (e) {
    return { error: String(e) }
  }
  return null
}

// toMs reads a time from the server: unix seconds or an ISO string.
function toMs(t: number | string): number {
  return typeof t === 'number' ? t * 1000 : Date.parse(t)
}

// watch runs the Connect work for a started app. onRoute gets {label, kind,
// better} for the sidebar. It returns {stop, streamState}: stop ends
// everything, and streamState goes to onEvent.
export function watch(onRoute: (notice: RouteNotice) => void): { stop(): void; streamState(up: boolean): void } {
  if (!link?.linked) return { stop() {}, streamState() {} }
  const machine = link.machine
  let stopped = false
  let isConnect = false
  let expires = 0
  let lastRenew = 0
  let renewTimer: ReturnType<typeof setTimeout> | undefined
  let routes: Route[] | null = null
  let current: Route | null = null
  let probeTimer: ReturnType<typeof setTimeout> | undefined
  let lostTimer: ReturnType<typeof setTimeout> | undefined
  let moving = false

  // Renewal: about 2 minutes before the access window ends, and never more
  // than once in 10 minutes unless the window is about to end.
  const arm = () => {
    clearTimeout(renewTimer)
    if (stopped || !isConnect) return
    let due = Math.max(expires - 120000, lastRenew + 600000)
    due = Math.min(due, expires - 15000)
    renewTimer = setTimeout(doRenew, Math.max(due - Date.now(), 1000))
  }
  const doRenew = async () => {
    lastRenew = Date.now()
    try {
      await renew()
    } catch {
      // The sign-in screen shows if the session is over. For a network
      // error, try again soon.
      if (!stopped) renewTimer = setTimeout(doRenew, 30000)
    }
  }
  onRenewed((res: { expires: number | string }) => {
    expires = toMs(res.expires)
    arm()
  })

  const better = async (all: Route[]): Promise<Route | null> => {
    const key = await pinnedKey()
    const found = await Promise.all(all.map(async (r) => ((await verifyHello(r.app, machine, key)) ? [r] : [])))
    return found.flat().sort((a, b) => rank(a) - rank(b))[0] ?? null
  }

  const probe = async () => {
    if (stopped || moving || !current || !routes) return
    const here = current
    const near = routes.filter((r) => rank(r) < rank(here) && r.app !== here.app && allowedRoute(r.app))
    if (!near.length) return
    const best = await better(near)
    if (!best || stopped) return
    onRoute({ label: ROUTE_LABEL[here.kind] ?? '', kind: here.kind, better: ROUTE_LABEL[best.kind] ?? '' })
    if (!isConnect || moving) return
    moving = true
    try {
      const { code } = await connectHandoff(best.app)
      const q = new URLSearchParams({ handoff: code, mid: machine, mk: await pinnedKey() })
      location.href = `${best.app}/connect#${q}`
    } catch {
      moving = false
    }
  }

  // The stream has been down for 20 seconds: look for any route that answers.
  const lostFor20 = async () => {
    if (stopped || !routes) return
    const others = routes.filter((r) => r.app !== location.origin && allowedRoute(r.app))
    const best = others.length ? await better(others) : null
    if (best && !stopped) location.href = `${best.app}/`
  }

  const visible = () => {
    if (document.visibilityState !== 'visible') return
    if (isConnect && expires - Date.now() < 300000 && Date.now() - lastRenew > 30000) doRenew()
    clearTimeout(probeTimer)
    probeTimer = setTimeout(probe, 500)
  }
  document.addEventListener('visibilitychange', visible)

  ;(async () => {
    try {
      const who = await whoami()
      isConnect = who.kind === 'connect'
      if (isConnect) {
        expires = toMs(who.expires ?? 0)
        lastRenew = Date.now()
        arm()
      }
      const found: Route[] = (await connectRoutes()).routes
      routes = found
      current = found.find((r) => r.app === location.origin) ?? null
      if (current) onRoute({ label: ROUTE_LABEL[current.kind] ?? '', kind: current.kind, better: '' })
      probeTimer = setTimeout(probe, 3000)
    } catch {}
  })()

  return {
    stop() {
      stopped = true
      clearTimeout(renewTimer)
      clearTimeout(probeTimer)
      clearTimeout(lostTimer)
      document.removeEventListener('visibilitychange', visible)
    },
    // The 20 second clock starts when the event stream is lost.
    streamState(up: boolean) {
      clearTimeout(lostTimer)
      if (!up) lostTimer = setTimeout(lostFor20, 20000)
    },
  }
}

// --- Web Push ---

export const pushSupported = (): boolean => features.push && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window

// registerWorker installs the push worker. It has no fetch handler and no
// cache, so it changes nothing about how the app loads.
export function registerWorker(): void {
  if (!features.push || !('serviceWorker' in navigator)) return
  navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {})
}

// pushState says whether this browser is subscribed, and the name choice.
export async function pushState(): Promise<{ on: boolean; names: boolean }> {
  const st = await pushStatus()
  const reg = await navigator.serviceWorker.ready
  const sub = await reg.pushManager.getSubscription()
  return { on: !!st.subscribed && !!sub, names: !!st.names }
}

export async function pushOn(): Promise<void> {
  if ((await Notification.requestPermission()) !== 'granted') throw 'Notifications are blocked for this site.'
  const st = await pushStatus()
  const reg = await navigator.serviceWorker.ready
  const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64uDecode(st.key) })
  const j = sub.toJSON()
  await pushSubscribe({ endpoint: j.endpoint, keys: j.keys }, false)
}

export async function pushOff(): Promise<void> {
  const reg = await navigator.serviceWorker.ready
  await (await reg.pushManager.getSubscription())?.unsubscribe()
  await pushUnsubscribe()
}

export { pushNames, pushTest }
