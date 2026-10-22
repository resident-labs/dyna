import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createReadMarker } from './readMarker.js'
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }
const post = (id) => ({ id, roomId: 'a', unread: true })

test('read markers deduplicate seen IDs and collect new IDs into one trailing batch', async () => {
  const waits = [deferred(), deferred()], calls = []
  const marker = createReadMarker({
    markReadMany: (room, ids) => { calls.push({ room, ids }); return waits[calls.length - 1].promise },
  }, { current: () => ({ room: 'a', visit: 1, visible: true }), read() {} })
  const first = post('one'), second = post('two'), third = post('three')
  const requests = [marker.mark([first])]
  for (let i = 0; i < 10; i++) requests.push(marker.mark([first]))
  requests.push(marker.mark([first, second]), marker.mark([first, third]))
  assert.deepEqual(calls, [{ room: 'a', ids: ['one'] }])
  waits[0].resolve()
  await new Promise(setImmediate)
  assert.deepEqual(calls[1], { room: 'a', ids: ['two', 'three'] })
  waits[1].resolve()
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
  const marker = createReadMarker({ markReadMany: () => { calls++; return calls === 1 ? wait.promise : Promise.resolve() } }, {
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
  const marker = createReadMarker({ markReadMany: async () => { if (++calls === 1) throw new Error('offline') } }, {
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
  const wait = deferred(), calls = []
  let visible = true
  const marker = createReadMarker({ markReadMany: (room, ids) => { calls.push(ids); return calls.length === 1 ? wait.promise : Promise.resolve() } }, {
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
