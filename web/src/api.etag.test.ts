import { test } from 'node:test'
import assert from 'node:assert/strict'

// api.js reads the page origin and CSP meta tag when it loads.
Object.assign(globalThis, { location: { origin: 'https://dyna.test' }, document: { querySelector: () => ({ content: '{}' }) } })

interface Api {
  feedTail(room: string, before: string, keep: number): Promise<unknown>
  rooms(): Promise<unknown>
}

function isApi(value: unknown): value is { api: Api } {
  return typeof value === 'object' && value !== null && 'api' in value && typeof value.api === 'object' && value.api !== null && 'feedTail' in value.api && 'rooms' in value.api
}

// Each test loads its own copy, so the kept replies of one never reach another.
async function load(tag: string): Promise<Api> {
  const module: unknown = await import(`./api.js?${tag}`)
  assert.ok(isApi(module))
  return module.api
}

interface Seen {
  path: string
  ifNoneMatch: string | null
}

function serve(replies: (call: number) => Response): Seen[] {
  const seen: Seen[] = []
  globalThis.fetch = async (input, init) => {
    const headers = new Headers(init?.headers)
    seen.push({ path: String(input), ifNoneMatch: headers.get('If-None-Match') })
    return replies(seen.length)
  }
  return seen
}

test('FeedTail sends the last ETag back and reuses the old reply on 304', async () => {
  const api = await load('etag-304')
  const seen = serve((call) => (call === 1 ? Response.json({ posts: ['a'] }, { headers: { ETag: '"one"' } }) : new Response(null, { status: 304, headers: { ETag: '"one"' } })))
  const first = await api.feedTail('room', '', 50)
  const second = await api.feedTail('room', '', 50)
  assert.deepEqual(seen.map((s) => s.ifNoneMatch), [null, '"one"'])
  assert.deepEqual(second, { posts: ['a'] })
  assert.notEqual(second, first, 'each caller gets its own copy')
})

test('a changed FeedTail reply replaces the kept one', async () => {
  const api = await load('etag-changed')
  const seen = serve((call) => Response.json({ n: call }, { headers: { ETag: `"v${call}"` } }))
  await api.feedTail('room', '', 50)
  assert.deepEqual(await api.feedTail('room', '', 50), { n: 2 })
  await api.feedTail('room', '', 50)
  assert.deepEqual(seen.map((s) => s.ifNoneMatch), [null, '"v1"', '"v2"'])
})

test('a different FeedTail request does not borrow another request\'s ETag', async () => {
  const api = await load('etag-keys')
  const seen = serve(() => Response.json({}, { headers: { ETag: '"x"' } }))
  await api.feedTail('room', '', 50)
  await api.feedTail('room', 'p9', 50)
  assert.deepEqual(seen.map((s) => s.ifNoneMatch), [null, null])
})

test('other calls read the reply as JSON and never as text', async () => {
  const api = await load('etag-json')
  serve(() => {
    const reply = Response.json(['room'])
    reply.text = () => Promise.reject(new Error('text was read'))
    return reply
  })
  assert.deepEqual(await api.rooms(), ['room'])
})

test('other calls send no If-None-Match even after an ETag was seen', async () => {
  const api = await load('etag-other')
  const seen = serve(() => Response.json([], { headers: { ETag: '"x"' } }))
  await api.rooms()
  await api.rooms()
  assert.deepEqual(seen.map((s) => s.ifNoneMatch), [null, null])
})
