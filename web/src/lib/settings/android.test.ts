import test from 'node:test'
import assert from 'node:assert/strict'
import { appLink, hasApp, pairLink } from './android.ts'

test('the download link and the pairing link carry the address and code', () => {
  assert.equal(appLink('https://m.example.dev/'), 'https://m.example.dev/api/android/app.apk')
  assert.equal(pairLink('http://100.64.0.1:7457/', 'ABC 123'), 'dyna://pair?url=http%3A%2F%2F100.64.0.1%3A7457&code=ABC%20123')
})

test('the app is offered only when the engine serves a build that opens the pairing link', async () => {
  const serve = (ok: boolean, body: unknown) => (async () => ({ ok, json: async () => body }) as Response) as typeof fetch
  assert.equal(await hasApp(serve(true, { versionCode: 2, protocol: 1 })), true)
  assert.equal(await hasApp(serve(true, { versionCode: 3 })), true)
  assert.equal(await hasApp(serve(true, { versionCode: 1 })), false) // an older app cannot open dyna://pair
  assert.equal(await hasApp(serve(true, {})), false)
  assert.equal(await hasApp(serve(true, null)), false)
  assert.equal(await hasApp(serve(true, { versionCode: '2' })), false)
  assert.equal(await hasApp(serve(false, { versionCode: 2 })), false)
  assert.equal(await hasApp((async () => { throw new Error('offline') }) as typeof fetch), false)
})
