import { test } from 'node:test'
import assert from 'node:assert/strict'

const ev = (data, start, more = {}) => ({ id: 'term_test', data: btoa(data), start, offset: start + data.length, exited: false, exitCode: 0, ...more })

test('web attach decodes split frames and preserves raw binary bytes', async () => {
  globalThis.document = { querySelector: () => ({ content: JSON.stringify({ web: true }) }) }
  const binary = '\x00\xff\xc3\xa9'
  const frame = `event: hello\ndata: {}\n\ndata: ${JSON.stringify(ev(binary, 0))}\n\ndata: ${JSON.stringify(ev('', binary.length, { exited: true, exitCode: 7 }))}\n\n`
  globalThis.fetch = async (url, options) => {
    assert.equal(url, '/api/terminal/attach')
    assert.equal(options.method, 'POST')
    assert.equal(options.headers['X-Dyna'], '1')
    assert.deepEqual(JSON.parse(options.body), { id: 'term_test', after: 0 })
    const bytes = new TextEncoder().encode(frame)
    return new Response(new ReadableStream({ start(c) { c.enqueue(bytes.slice(0, 41)); c.enqueue(bytes.slice(41)); c.close() } }))
  }
  const { api } = await import('./api.ts?web-test')
  const received = []
  let finish
  const complete = new Promise((resolve) => { finish = resolve })
  const detach = await api.termAttach('term_test', (e) => { received.push(e); if (e.exited) finish() })
  await complete
  assert.equal(received.map((e) => atob(e.data)).join(''), binary)
  assert.equal(received.at(-1).exitCode, 7)
  detach()
})
