import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { TermOutput } from './lib/api/terminalTransport.ts'

// api.ts reads the CSP meta tag when it loads, so the fake page comes first.
Object.assign(globalThis, { document: { querySelector: () => ({ content: JSON.stringify({ web: true }) }) } })

const ev = (data: string, start: number, more: Partial<TermOutput> = {}): TermOutput => ({ id: 'term_test', data: btoa(data), start, offset: start + data.length, exited: false, exitCode: 0, ...more })

interface ApiModule {
  api: { termAttach: (id: string, output: (ev: TermOutput) => void) => Promise<() => void> }
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null
// The test loads its own copy, so the loaded module is checked before use.
const isApiModule = (value: unknown): value is ApiModule => isRecord(value) && isRecord(value.api) && typeof value.api.termAttach === 'function'

async function loadApi(tag: string): Promise<ApiModule> {
  const module: unknown = await import(`./api.ts?${tag}`)
  assert.ok(isApiModule(module))
  return module
}

test('web attach decodes split frames and preserves raw binary bytes', async () => {
  const binary = '\x00\xff\xc3\xa9'
  const frame = `event: hello\ndata: {}\n\ndata: ${JSON.stringify(ev(binary, 0))}\n\ndata: ${JSON.stringify(ev('', binary.length, { exited: true, exitCode: 7 }))}\n\n`
  globalThis.fetch = async (input, init) => {
    assert.equal(String(input), '/api/terminal/attach')
    assert.equal(init?.method, 'POST')
    assert.equal(new Headers(init?.headers).get('X-Dyna'), '1')
    assert.deepEqual(JSON.parse(String(init?.body)), { id: 'term_test', after: 0 })
    const bytes = new TextEncoder().encode(frame)
    return new Response(new ReadableStream({ start(c) { c.enqueue(bytes.slice(0, 41)); c.enqueue(bytes.slice(41)); c.close() } }))
  }
  const { api } = await loadApi('web-test')
  const received: TermOutput[] = []
  let finish: () => void = () => {}
  const complete = new Promise<void>((resolve) => { finish = resolve })
  const detach = await api.termAttach('term_test', (e) => { received.push(e); if (e.exited) finish() })
  await complete
  assert.equal(received.map((e) => atob(e.data ?? '')).join(''), binary)
  assert.equal(received.at(-1)?.exitCode, 7)
  detach()
})
