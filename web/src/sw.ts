// The Dyna push worker. It shows a notification for each push and opens the
// right room when you tap it. It has no fetch handler and caches nothing, so
// it changes nothing about how the app loads.
//
// The browser loads this file as a classic worker, so it must stay a script:
// no import and no export.

if (!(self instanceof ServiceWorkerGlobalScope)) throw new Error('sw.ts runs only in a service worker')
const worker: ServiceWorkerGlobalScope = self

// JSON starts unknown. Like a property read, this throws for null and
// undefined and gives undefined for a missing field of any other value.
function readFields(value: unknown): Record<'t' | 'title' | 'body' | 'room' | 'post' | 'call', unknown> {
  if (value === null || value === undefined) throw new TypeError('the payload has no fields')
  const boxed: object = Object(value)
  const get = (key: string): unknown => Reflect.get(boxed, key)
  return { t: get('t'), title: get('title'), body: get('body'), room: get('room'), post: get('post'), call: get('call') }
}

// A falsy field means "missing"; any other value is converted to text.
function textOr(value: unknown, fallback: string): string {
  return value ? String(value) : fallback
}

worker.addEventListener('install', () => void worker.skipWaiting())
worker.addEventListener('activate', (e) => e.waitUntil(worker.clients.claim()))

worker.addEventListener('push', (event) => {
  let payload: unknown = {}
  try {
    payload = event.data ? event.data.json() : {}
  } catch {}
  const d = readFields(payload)
  const room = textOr(d.room, 'dyna')
  const call = d.t === 'call'
  // Each question keeps its own tag. Failures share one tag per room, so a
  // burst replaces the previous failure instead of stacking. Calls, questions
  // and failures stay until dismissed.
  const pinned = call || d.t === 'question' || d.t === 'failure'
  let tag = room
  if (call) tag = 'call:' + room
  else if (d.t === 'question') tag = 'question:' + textOr(d.post, room)
  else if (d.t === 'failure') tag = 'failure:' + room
  event.waitUntil(
    worker.registration.showNotification(textOr(d.title, 'Dyna'), {
      body: textOr(d.body, 'New post in Dyna'),
      tag,
      requireInteraction: pinned,
      icon: '/icon-192.png',
      data: { room: d.room, post: d.post, call: d.call },
    }),
  )
})

// Only ids in the expected shape reach the URL. The payload never supplies a
// URL, and the URL always points at this worker's own origin.
const ROOM = /^room_[0-9]{8}-[0-9]{6}-[0-9a-f]{8}$/
const POST = /^[A-Za-z0-9_-]{1,64}$/
const CALL = /^c[0-9]{1,6}$/

worker.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const d = readFields(event.notification.data || {})
  const room = ROOM.test(textOr(d.room, '')) ? d.room : ''
  const post = room && POST.test(textOr(d.post, '')) ? d.post : ''
  const call = room && typeof d.call === 'string' && CALL.test(d.call) ? d.call : ''
  const url = worker.location.origin + '/' + (room ? '#room=' + String(room) + (post ? '&post=' + String(post) : '') + (call ? '&call=' + call : '') : '')
  event.waitUntil(
    worker.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
      const open = list.find((c) => new URL(c.url).origin === worker.location.origin)
      if (open) {
        open.postMessage({ type: 'open', room, post, call })
        return open.focus()
      }
      return worker.clients.openWindow(url)
    }),
  )
})
