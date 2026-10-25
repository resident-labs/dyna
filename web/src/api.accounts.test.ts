import { test } from 'node:test'
import assert from 'node:assert/strict'

// api.ts reads the page origin and CSP meta tag when it loads.
Object.assign(globalThis, { location: { origin: 'https://dyna.test' }, document: { querySelector: () => ({ content: '{}' }) } })

interface Api {
  accountsAdd(harness: string, provider: string, label: string, key: string): Promise<unknown>
}

function isApi(value: unknown): value is { api: Api } {
  return typeof value === 'object' && value !== null && 'api' in value && typeof value.api === 'object' && value.api !== null && 'accountsAdd' in value.api
}

async function load(tag: string): Promise<Api> {
  const module: unknown = await import(`./api.ts?${tag}`)
  assert.ok(isApi(module))
  return module.api
}

// The engine reads (harness, provider, label, key), so the wire order must match
// it: a swapped argument is rejected as an unknown provider, not a type error.
test('AccountsAdd sends the harness first, as the engine reads it', async () => {
  const api = await load('accounts-add-wire')
  const seen: { path: string; body: unknown }[] = []
  globalThis.fetch = async (input, init) => {
    seen.push({ path: String(input), body: JSON.parse(String(init?.body)) })
    return Response.json({ providers: [] })
  }
  await api.accountsAdd('opencode', 'opencode-go', 'Go work', 'sk-go')
  assert.deepEqual(seen, [{ path: '/api/AccountsAdd', body: ['opencode', 'opencode-go', 'Go work', 'sk-go'] }])
})
