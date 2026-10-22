import test from 'node:test'
import assert from 'node:assert/strict'
import { createTestServer } from '../../../test/ssr-server.ts'

interface FailedCallProps {
  command?: string
  output?: string
  exit?: number | null
}

test('a failed call panel shows its command, error text and exit code, never an empty box', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: FailedCall } = await vite.ssrLoadModule('/src/lib/sessions/FailedCall.svelte')
  const html = (props: FailedCallProps): string => render(FailedCall, { props }).body.replace(/<!--[\s\S]*?-->/g, '')
  const body = html({ command: 'dyna post @root "hi"', output: 'Error: database is locked (SQLITE_BUSY)', exit: 1 })
  assert.match(body, /<b[^>]*>\$<\/b>dyna post @root "hi"/)
  assert.match(body, /<pre[^>]*>Error: database is locked \(SQLITE_BUSY\)<\/pre>/)
  assert.match(body, /<small[^>]*>exit 1<\/small>/)
  const bare = html({})
  assert.match(bare, /<pre[^>]*>No output was captured\.<\/pre>/)
  assert.match(bare, /<small[^>]*>failed<\/small>/)
  assert.doesNotMatch(bare, /class="cmd/)
})
