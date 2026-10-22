import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { stripTypes } from '../../../test/svelte-source.ts'

const room = 'room_20261020-103000-0a1b2c3d'

function runWorker(self: object): void {
  class ServiceWorkerGlobalScope {}
  Object.setPrototypeOf(self, ServiceWorkerGlobalScope.prototype)
  const source = stripTypes(readFileSync(new URL('../../sw.ts', import.meta.url), 'utf8'))
  vm.runInNewContext(source, { self, URL, ServiceWorkerGlobalScope })
}

// tap runs sw.ts against a fake worker scope and returns what a tap on a
// notification with this data opened or posted.
interface OpenWindow {
  url: string
  postMessage(message: unknown): void
  focus(): Promise<void>
}
interface TapEvent {
  notification: { close(): void; data: unknown }
  waitUntil(promise: Promise<unknown>): void
}
function tap(data: unknown, windows: OpenWindow[] = []) {
  const handlers: Record<string, (event: TapEvent) => void> = {}
  const opened: string[] = []
  const self = {
    location: { origin: 'https://dyna.test' },
    addEventListener: (type: string, fn: (event: TapEvent) => void) => (handlers[type] = fn),
    skipWaiting() {},
    clients: { claim() {}, matchAll: async () => windows, openWindow: async (url: string) => opened.push(url) },
  }
  runWorker(self)
  let done: Promise<unknown> = Promise.resolve()
  handlers.notificationclick?.({ notification: { close() {}, data }, waitUntil: (p) => (done = p) })
  return done.then(() => opened)
}

test('a push tap opens the room and the call request', async () => {
  assert.deepEqual(await tap({ room, call: 'c7' }), [`https://dyna.test/#room=${room}&call=c7`])
  assert.deepEqual(await tap({ room, post: 'p1', call: 'c12' }), [`https://dyna.test/#room=${room}&post=p1&call=c12`])
})

test('an invalid call ID never reaches the URL', async () => {
  for (const call of ['7', 'c', 'c1234567', 'c7&x=1', 'C7', '../c7', 7, ['c7']]) {
    assert.deepEqual(await tap({ room, call }), [`https://dyna.test/#room=${room}`], String(call))
  }
  assert.deepEqual(await tap({ room: 'nope', call: 'c7' }), ['https://dyna.test/'], 'no valid room, no call')
})

test('an open window gets the call ID by message', async () => {
  const sent: unknown[] = []
  const win = { url: 'https://dyna.test/', postMessage: (m: unknown) => sent.push(JSON.parse(JSON.stringify(m))), focus: async () => {} }
  await tap({ room, call: 'c7' }, [win])
  await tap({ room, call: 'bad' }, [win])
  assert.deepEqual(sent, [{ type: 'open', room, post: '', call: 'c7' }, { type: 'open', room, post: '', call: '' }])
})

async function push(data: unknown) {
  const handlers: Record<string, (event: { data: { json(): unknown }; waitUntil(p: Promise<unknown>): void }) => void> = {}
  const notifications: { title: string; options: NotificationOptions }[] = []
  const self = {
    addEventListener: (type: string, fn: typeof handlers[string]) => (handlers[type] = fn),
    registration: {
      showNotification: async (title: string, options: NotificationOptions) => { notifications.push({ title, options }) },
    },
  }
  runWorker(self)
  let done: Promise<unknown> = Promise.resolve()
  handlers.push?.({ data: { json: () => data }, waitUntil: (p) => (done = p) })
  await done
  const notification = notifications[0]
  assert.ok(notification)
  return notification.options
}

test('call pushes stay visible and use a different tag from room posts', async () => {
  const call = await push({ t: 'call', room, call: 'c7' })
  const post = await push({ room, post: 'p1' })
  assert.equal(call.tag, 'call:' + room)
  assert.equal(call.requireInteraction, true)
  assert.equal(post.tag, room)
  assert.notEqual(call.tag, post.tag)
  assert.notEqual(post.requireInteraction, true)
})
