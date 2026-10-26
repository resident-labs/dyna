import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createReadMarker } from './readMarker.ts'
import { need } from '../../../test/need.ts'

function deferred() {
  let resolve: () => void = () => {}
  let reject: (reason?: unknown) => void = () => {}
  const promise = new Promise<void>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const post = (id: string) => ({ id, roomId: 'a', unread: true })
const noRoomRead = async () => {}

test('viewing the room chat clears the whole room once per visit, and other views do not', async () => {
  const cleared: string[] = []
  let value = { room: 'a', visit: 1, visible: true, chat: true }, updates = 0
  const marker = createReadMarker({ markReadMany: async () => {}, markRoomRead: async (room) => { cleared.push(room) } }, { current: () => value, read: () => updates++ })
  await marker.mark([])
  await marker.mark([])
  assert.deepEqual(cleared, ['a'])
  assert.equal(updates, 1)
  value = { room: 'b', visit: 2, visible: true, chat: false }
  await marker.mark([])
  assert.deepEqual(cleared, ['a'])
  value = { room: 'b', visit: 3, visible: true, chat: true }
  await marker.mark([])
  assert.deepEqual(cleared, ['a', 'b'])
})

test('read markers deduplicate seen IDs and collect new IDs into one trailing batch', async () => {
  const waits = [deferred(), deferred()], calls: { room: string; ids: string[] }[] = []
  const marker = createReadMarker({
    markReadMany: (room, ids) => { calls.push({ room, ids }); return need(waits[calls.length - 1]).promise }, markRoomRead: noRoomRead,
  }, { current: () => ({ room: 'a', visit: 1, visible: true }), read() {} })
  const first = post('one'), second = post('two'), third = post('three')
  const requests = [marker.mark([first])]
  for (let i = 0; i < 10; i++) requests.push(marker.mark([first]))
  requests.push(marker.mark([first, second]), marker.mark([first, third]))
  assert.deepEqual(calls, [{ room: 'a', ids: ['one'] }])
  need(waits[0]).resolve()
  await new Promise(setImmediate)
  assert.deepEqual(calls[1], { room: 'a', ids: ['two', 'three'] })
  need(waits[1]).resolve()
  await Promise.all(requests)
  assert.equal(first.unread, false)
  assert.equal(second.unread, false)
  assert.equal(third.unread, false)
  assert.equal(marker.mark([post('one')]), null)
  assert.equal(calls.length, 2)
})
test('read markers drop closed views and can retry a failed visible write', async () => {
  const wait = deferred()
  let value = { room: 'a', visit: 1, visible: true }, calls = 0, updates = 0
  const marker = createReadMarker({ markReadMany: () => { calls++; return calls === 1 ? wait.promise : Promise.resolve() }, markRoomRead: noRoomRead }, {
    current: () => value, read: () => updates++,
  })
  const first = post('one'), second = post('two')
  const old = marker.mark([first]); marker.mark([second])
  value = { room: 'a', visit: 2, visible: false }
  wait.resolve(); await old
  assert.equal(calls, 1)
  assert.equal(updates, 0)
  assert.equal(first.unread, true)
  value = { room: 'a', visit: 3, visible: true }
  await marker.mark([first])
  assert.equal(calls, 2)
  assert.equal(first.unread, false)
  assert.equal(updates, 1)
})
test('failed read writes leave IDs available for a later refresh', async () => {
  let calls = 0
  const marker = createReadMarker({ markReadMany: async () => { if (++calls === 1) throw new Error('offline') }, markRoomRead: noRoomRead }, {
    current: () => ({ room: 'a', visit: 1, visible: true }), read() {},
  })
  const first = post('one')
  await marker.mark([first])
  assert.equal(first.unread, true)
  await marker.mark([first])
  assert.equal(first.unread, false)
  assert.equal(calls, 2)
})
test('an unread batch dropped while hidden is eligible when the same channel reopens', async () => {
  const wait = deferred(), calls: string[][] = []
  let visible = true
  const marker = createReadMarker({ markReadMany: (room, ids) => { calls.push(ids); return calls.length === 1 ? wait.promise : Promise.resolve() }, markRoomRead: noRoomRead }, {
    current: () => ({ room: 'a', visit: 1, visible }), read() {},
  })
  const first = post('one'), second = post('two')
  const old = marker.mark([first]); marker.mark([second])
  visible = false
  assert.equal(marker.mark([first, second]), null)
  wait.resolve(); await old
  visible = true
  await marker.mark([first, second])
  assert.deepEqual(calls, [['one'], ['one', 'two']])
  assert.equal(first.unread, false)
  assert.equal(second.unread, false)
})
