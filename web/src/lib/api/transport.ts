// HTTP requests, Connect renewal, and feed reply revalidation.
import { deviceKey, makeProof } from '../settings/connect.ts'
import { arrayOf, isBoolean, isRecord, isString, optional } from './guards.ts'
import type { ConnectRoute, ConnectSession, Handoff, PushStatus, Renewal, RouteList, Whoami } from './connect.ts'

// proof signs a request to path with this browser's device key.
async function proof(path: string) {
  return makeProof(await deviceKey(), 'POST', location.origin + path)
}

// Each API client keeps its own callbacks, pending renewal, and feed replies.
export function createTransport() {
  let unauthorized = () => {}
  // onUnauthorized runs when the server says this browser is not paired.
  function onUnauthorized(fn: () => void) {
    unauthorized = fn
  }

  // A Connect session has a short access window. A 401 with code "renew"
  // means the session can renew: renew once (calls share one renewal), then
  // send the call again once.
  let renewing: Promise<Renewal | null> | null = null
  let renewed = (_info: Renewal) => {}
  // onRenewed runs with {expires, grantEnd} after each renewal.
  function onRenewed(fn: (info: Renewal) => void) {
    renewed = fn
  }

  // errorOf is what a failed call throws: the server message, or the status.
  function errorOf(data: unknown, status: number): unknown {
    return (isRecord(data) ? data.error : undefined) ?? `HTTP ${status}`
  }

  function isRenewal(v: unknown): v is Renewal {
    return isRecord(v) && isString(v.expires) && isString(v.grantEnd) && typeof v.grace === 'boolean'
  }

  function renew(): Promise<Renewal | null> {
    renewing ??= (async () => {
      try {
        const r = await fetch('/api/connect/renew', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json', 'X-Dyna': '1' },
          body: JSON.stringify({ proof: await proof('/api/connect/renew') }),
        })
        // 401: the session has ended. 403: this browser's key cannot renew
        // it. Both mean sign-in; anything else (relay down, busy) may pass,
        // so the session stays and renewal tries again later.
        if (r.status === 401 || r.status === 403) unauthorized()
        const data: unknown = await r.json().catch(() => null)
        if (!r.ok) throw errorOf(data, r.status)
        if (!isRenewal(data)) return null
        renewed(data)
        return data
      } finally {
        renewing = null
      }
    })()
    return renewing
  }

  // Keep the response body available for binary previews and streams. Callers
  // pass replayable bodies (JSON strings) and may cancel either attempt.
  async function authenticatedFetch(path: string, options: RequestInit = {}, canRenew = true): Promise<Response> {
    const request = () => fetch(path, { ...options, credentials: 'same-origin' })
    let response = await request()
    if (response.status === 401) {
      const error: unknown = await response.clone().json().catch(() => null)
      if (isRecord(error) && error.code === 'renew' && canRenew) {
        await renew()
        options.signal?.throwIfAborted()
        response = await request()
      }
      if (response.status === 401) unauthorized()
    }
    return response
  }

  // A room's feed is asked for again on every event and mostly comes back unchanged.
  // The server tags that reply with an ETag; a POST gets no browser cache, so the
  // last reply for each request is kept here and sent back with If-None-Match.
  const REVALIDATED = new Set(['/api/FeedTail'])
  const REVALIDATED_MAX = 32
  const replies = new Map<string, { etag: string; text: string }>()

  function parseOrNull(text: string): unknown {
    try {
      return JSON.parse(text)
    } catch {
      return null
    }
  }

  async function post(path: string, body: unknown, canRenew = true): Promise<unknown> {
    const payload = JSON.stringify(body)
    const key = REVALIDATED.has(path) ? path + payload : ''
    const known = key ? replies.get(key) : undefined
    const headers: Record<string, string> = { 'Content-Type': 'application/json', 'X-Dyna': '1' }
    if (known) headers['If-None-Match'] = known.etag
    const r = await authenticatedFetch(path, { method: 'POST', headers, body: payload }, canRenew)
    if (r.status === 304 && known) return JSON.parse(known.text)
    // Only a revalidated reply keeps its raw text; every other call reads JSON as before.
    const text = key ? await r.text().catch(() => '') : ''
    const data: unknown = key ? parseOrNull(text) : await r.json().catch(() => null)
    // Errors reject with the server message.
    if (!r.ok) throw errorOf(data, r.status)
    const etag = key ? r.headers.get('ETag') : null
    if (etag) {
      replies.delete(key)
      replies.set(key, { etag, text })
      if (replies.size > REVALIDATED_MAX) {
        const oldest = replies.keys().next()
        if (!oldest.done) replies.delete(oldest.value)
      }
    }
    return data
  }

  function isHandoff(v: unknown): v is Handoff {
    return isRecord(v) && isString(v.code)
  }

  function isConnectSession(v: unknown): v is ConnectSession {
    return isRecord(v) && isString(v.session) && isString(v.expires) && isString(v.grantEnd)
  }

  function isWhoami(v: unknown): v is Whoami {
    return isRecord(v) && isString(v.kind) && isString(v.id) && isString(v.name) && optional(v.user, isString) && optional(v.expires, isString) && optional(v.grantEnd, isString)
  }

  function isConnectRoute(v: unknown): v is ConnectRoute {
    return isRecord(v) && isString(v.kind) && isString(v.app) && isString(v.cards)
  }

  function isRouteList(v: unknown): v is RouteList {
    return isRecord(v) && isString(v.machine) && isString(v.key) && isString(v.current) && arrayOf(isConnectRoute)(v.routes)
  }

  function isPushStatus(v: unknown): v is PushStatus {
    return isRecord(v) && isBoolean(v.subscribed) && isBoolean(v.names) && isString(v.key)
  }

  // reply passes a reply that has the shape the guard names, or throws.
  function reply<T>(data: unknown, guard: (v: unknown) => v is T, path: string): T {
    if (!guard(data)) throw new Error(`${path} sent an unexpected reply`)
    return data
  }

  // connectSession trades a relay code or a route handoff for a session.
  // secret is {code} or {handoff}.
  async function connectSession(secret: { code: string } | { handoff: string }): Promise<ConnectSession> {
    return reply(await post('/api/connect/session', { ...secret, proof: await proof('/api/connect/session') }), isConnectSession, '/api/connect/session')
  }

  // connectHandoff asks for a one-time code to open a session on another route.
  async function connectHandoff(target: string): Promise<Handoff> {
    return reply(await post('/api/connect/handoff', { target, proof: await proof('/api/connect/handoff') }), isHandoff, '/api/connect/handoff')
  }

  const whoami = async () => reply(await post('/api/connect/whoami', []), isWhoami, '/api/connect/whoami')
  const connectRoutes = async () => reply(await post('/api/connect/routes', []), isRouteList, '/api/connect/routes')

  // Web Push: the server holds the keys and sends; the browser subscribes.
  const pushStatus = async () => reply(await post('/api/push/status', []), isPushStatus, '/api/push/status')
  const pushSubscribe = (sub: PushSubscriptionJSON, names = false) => post('/api/push/subscribe', { endpoint: sub.endpoint, keys: sub.keys, names })
  const pushNames = (names: boolean) => post('/api/push/names', { names })
  const pushUnsubscribe = () => post('/api/push/unsubscribe', {})
  const pushTest = () => post('/api/push/test', {})

  // pair trades a one-time code from `dyna engine run --pair` for this
  // browser's device cookie.
  function pair(code: string, name: string) {
    return post('/api/pair', { code, name })
  }

  // logout forgets this browser's device on the server.
  function logout() {
    return post('/api/logout', [])
  }

  return { post, renew, authenticatedFetch, onUnauthorized, onRenewed, connectSession, connectHandoff, whoami, connectRoutes, pushStatus, pushSubscribe, pushNames, pushUnsubscribe, pushTest, pair, logout }
}
