import test from 'node:test'
import assert from 'node:assert/strict'
import { createTestServer } from '../../../test/ssr-server.ts'

test('worker detail shows the result and then the journal, newest first', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: WorkerDetail } = await vite.ssrLoadModule('/src/lib/workflows/WorkerDetail.svelte')
  const value = {
    result: '{"summary":"Done."}',
    journal: [
      { ts: 2000, kind: 'progress', message: 'Searched the docs', next: 'Write the summary' },
      { ts: 1000, kind: 'orient', message: 'Read the task' },
    ],
  }
  const body = render(WorkerDetail, { props: { value } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /Done\./)
  assert.match(body, /<h3[^>]*>Journal<span[^>]*>2<\/span><\/h3>/)
  assert.ok(body.indexOf('Searched the docs') < body.indexOf('Read the task'), 'newest first')
  assert.match(body, /Next: Write the summary/)
  const running = render(WorkerDetail, { props: { value: { result: '', journal: value.journal } } }).body
  assert.doesNotMatch(running, /Done\./)
  assert.match(running, /Journal/)
})
