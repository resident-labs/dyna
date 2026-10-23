import { test } from 'node:test'
import assert from 'node:assert/strict'

// Renewal signs with a real temporary key. The IndexedDB fake holds no data.
globalThis.location = { origin: 'https://dyna.test' }
globalThis.document = { querySelector: () => ({ content: '{}' }) }
const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign'])
const request = (result) => {
  const req = { result }
  queueMicrotask(() => req.onsuccess())
  return req
}
globalThis.indexedDB = { open: () => request({ transaction: () => ({ objectStore: () => ({ get: () => request(pair) }) }) }) }
const json = (body, status = 200) => Response.json(body, { status })

test('concurrent binary requests share renewal and retain their response bodies', async () => {
  const { authenticatedFetch, onRenewed } = await import('./api.ts?auth-shared')
  let renewals = 0, renewed = 0
  const attempts = new Map()
  const controller = new AbortController()
  onRenewed(() => renewed++)
  globalThis.fetch = async (path, options) => {
    assert.equal(options.credentials, 'same-origin')
    if (path === '/api/connect/renew') { renewals++; return json({ expires: '2026-10-23T13:00:00Z', grantEnd: '2026-10-24T13:00:00Z', grace: false }) }
    assert.equal(options.signal, controller.signal)
    assert.equal(options.cache, 'no-store')
    const count = (attempts.get(path) || 0) + 1
    attempts.set(path, count)
    return count === 1 ? json({ code: 'renew' }, 401) : new Response(new Uint8Array([0, 255, 123]))
  }
  const responses = await Promise.all(['/attachments/a/one', '/attachments/a/two'].map((path) => authenticatedFetch(path, { signal: controller.signal, cache: 'no-store' })))
  for (const response of responses) assert.deepEqual([...new Uint8Array(await response.arrayBuffer())], [0, 255, 123])
  assert.equal(renewals, 1)
  assert.equal(renewed, 1)
  assert.deepEqual([...attempts.values()], [2, 2])
})

test('a second 401 ends renewal and reports unauthorized once', async () => {
  const { authenticatedFetch, onUnauthorized } = await import('./api.ts?auth-bounded')
  let renewals = 0, attempts = 0, refused = 0
  onUnauthorized(() => refused++)
  globalThis.fetch = async (path) => {
    if (path === '/api/connect/renew') { renewals++; return json({}) }
    attempts++
    return json({ code: 'renew' }, 401)
  }
  assert.equal((await authenticatedFetch('/attachments/a/one')).status, 401)
  assert.equal(renewals, 1)
  assert.equal(attempts, 2)
  assert.equal(refused, 1)
})

test('renewal refusal cannot recursively renew itself', async () => {
  const { authenticatedFetch, onUnauthorized } = await import('./api.ts?auth-refused')
  let calls = 0, refused = 0
  onUnauthorized(() => refused++)
  globalThis.fetch = async () => { calls++; return json({ code: 'renew', error: 'Pair again' }, 401) }
  await assert.rejects(authenticatedFetch('/attachments/a/one'), (error) => error === 'Pair again')
  assert.equal(calls, 2)
  assert.equal(refused, 1)
})

test('cancellation during renewal prevents replay', async () => {
  const { authenticatedFetch } = await import('./api.ts?auth-aborted')
  const controller = new AbortController()
  let attempts = 0
  globalThis.fetch = async (path) => {
    if (path === '/api/connect/renew') { controller.abort(); return json({}) }
    attempts++
    return json({ code: 'renew' }, 401)
  }
  await assert.rejects(authenticatedFetch('/attachments/a/one', { signal: controller.signal }), { name: 'AbortError' })
  assert.equal(attempts, 1)
})

test('ordinary 401 and non-JSON failures keep the body readable', async () => {
  const { authenticatedFetch, onUnauthorized } = await import('./api.ts?auth-errors')
  let refused = 0
  onUnauthorized(() => refused++)
  for (const status of [401, 500]) {
    globalThis.fetch = async () => new Response('Unavailable', { status })
    const response = await authenticatedFetch('/attachments/a/one')
    assert.equal(response.status, status)
    assert.equal(await response.text(), 'Unavailable')
  }
  assert.equal(refused, 1)
})

test('the data folder comes from the server for every client', async () => {
  const { api } = await import('./api.ts?data-folder')
  globalThis.fetch = async (path, options) => {
    assert.equal(path, '/api/DataDir')
    assert.deepEqual(JSON.parse(options.body), [])
    return json('/home/test/.local/share/dyna')
  }
  assert.equal(await api.dataDir(), '/home/test/.local/share/dyna')
})

test('a refused renewal (403) reports unauthorized instead of looping silently', async () => {
  const { authenticatedFetch, onUnauthorized } = await import('./api.ts?auth-renew-refused')
  let refused = 0, renewals = 0
  onUnauthorized(() => refused++)
  globalThis.fetch = async (path) => {
    if (path === '/api/connect/renew') { renewals++; return json({ error: 'the proof is from another key' }, 403) }
    return json({ code: 'renew' }, 401)
  }
  await assert.rejects(authenticatedFetch('/attachments/a/one'), (error) => error === 'the proof is from another key')
  assert.equal(renewals, 1)
  assert.equal(refused, 1, 'a session whose key cannot renew must show sign-in at once')
})

test('a busy relay (503) keeps the session and shows nothing', async () => {
  const { authenticatedFetch, onUnauthorized } = await import('./api.ts?auth-renew-busy')
  let refused = 0
  onUnauthorized(() => refused++)
  globalThis.fetch = async (path) => path === '/api/connect/renew'
    ? json({ error: 'the Resident Labs relay cannot be reached' }, 503)
    : json({ code: 'renew' }, 401)
  await assert.rejects(authenticatedFetch('/attachments/a/one'))
  assert.equal(refused, 0, 'a transient renew failure must not log the owner out')
})
