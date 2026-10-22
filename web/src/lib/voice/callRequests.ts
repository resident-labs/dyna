// Explicit call requests: root rings the owner with `dyna call`. The engine
// keeps each request (internal/callrequests) and sends changes as
// "call-requests" list events. A post to @voice-call never rings.
import { writable } from 'svelte/store'
import { isRecord, isString } from '../api/guards.ts'
import { roomIDPattern } from '../ui/posts.js'

export type CallState = 'ringing' | 'joined' | 'declined' | 'withdrawn'

export interface CallRequest {
  id: string
  room: string
  reason: string
  state: CallState
  askedAt: string
}

// The push worker and the app accept only IDs in this shape.
export const CALL_ID = /^c[0-9]{1,6}$/

const states: readonly string[] = ['ringing', 'joined', 'declined', 'withdrawn']

export function isCallRequest(v: unknown): v is CallRequest {
  return isRecord(v) && isString(v.id) && CALL_ID.test(v.id) && isString(v.room) && isString(v.reason) && isString(v.askedAt) && isString(v.state) && states.includes(v.state)
}

// requestsIn flattens CallRequests' answer: requests grouped by room ID.
export function requestsIn(grouped: unknown): CallRequest[] {
  if (!isRecord(grouped)) return []
  return Object.values(grouped).flatMap((rows) => (Array.isArray(rows) ? rows.filter(isCallRequest) : []))
}

// requestIn reads one request from a live event, or null for any other event.
export function requestIn(ev: unknown): CallRequest | null {
  if (!isRecord(ev) || ev.kind !== 'call-requests' || !isRecord(ev.change) || ev.change.op !== 'upsert') return null
  return isCallRequest(ev.change.item) ? ev.change.item : null
}

const same = (a: CallRequest, b: CallRequest) => a.room === b.room && a.id === b.id

export function withRequest(list: CallRequest[], req: CallRequest): CallRequest[] {
  return list.some((r) => same(r, req)) ? list.map((r) => (same(r, req) ? req : r)) : [...list, req]
}

// ringing is the oldest request that still rings.
export function ringing(list: CallRequest[]): CallRequest | null {
  return list.filter((r) => r.state === 'ringing').sort((a, b) => Date.parse(a.askedAt) - Date.parse(b.askedAt))[0] ?? null
}

// callLink names the request a push tap opened, or null for an ID that is
// not a call ID.
export function callLink(room: unknown, call: unknown): { room: string; call: string } | null {
  return isString(room) && roomIDPattern.test(room) && isString(call) && CALL_ID.test(call) ? { room, call } : null
}

// callTarget reads a push link, #room=<room>&call=<id>.
export function callTarget(hash: string): { room: string; call: string } | null {
  const h = new URLSearchParams(hash.replace(/^#/, ''))
  return callLink(h.get('room'), h.get('call'))
}

export interface CallRequestsApi {
  callRequests(roomId: string): Promise<unknown>
  callRequestShown(roomId: string, id: string): Promise<unknown>
  declineCallRequest(roomId: string, id: string): Promise<unknown>
}

// createCallRequests holds every request the app knows. The widget rings
// from it and the room feed shows its rows.
export function createCallRequests(api: CallRequestsApi) {
  let list: CallRequest[] = []
  const store = writable<CallRequest[]>(list)
  const reported = new Set<string>()
  const put = (next: CallRequest[]) => store.set((list = next))
  const key = (req: CallRequest) => `${req.room}/${req.id}`

  // load reads the rooms' open requests, or one room's whole history. A row
  // read here never reverts a newer state from an event.
  async function load(roomId = ''): Promise<void> {
    const before = list
    const rows = requestsIn(await api.callRequests(roomId))
    if (!roomId) {
      // The global snapshot contains only open requests. Read a missing
      // request's room to retain its closed row after a missed event.
      const missing = new Set(before.filter((r) => r.state === 'ringing' &&
        !rows.some((row) => same(row, r))).map((r) => r.room))
      const histories = await Promise.all([...missing].map((room) => api.callRequests(room).catch(() => ({}))))
      rows.push(...histories.flatMap(requestsIn))
    }
    // A reconnect must clear rings that closed while the stream was down.
    // Preserve changes that arrived while this snapshot was in flight.
    let next = list.filter((r) => r.state !== 'ringing' || (roomId && r.room !== roomId) ||
      !before.includes(r) || rows.some((row) => same(row, r)))
    for (const row of rows) {
      const current = next.find((r) => same(r, row))
      if (current && (!before.includes(current) || current.state !== 'ringing')) continue
      next = withRequest(next, row)
    }
    put(next)
  }

  function apply(ev: unknown): void {
    const req = requestIn(ev)
    if (req) put(withRequest(list, req))
  }

  // shown tells the engine that the app showed the request, once per request.
  async function shown(req: CallRequest): Promise<void> {
    if (reported.has(key(req))) return
    reported.add(key(req))
    await api.callRequestShown(req.room, req.id).catch(() => reported.delete(key(req)))
  }

  async function decline(req: CallRequest): Promise<void> {
    const closed = await api.declineCallRequest(req.room, req.id)
    if (!isCallRequest(closed)) throw new Error('Invalid call request response')
    put(withRequest(list, closed))
  }

  return { subscribe: store.subscribe, load, apply, shown, decline }
}
