import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { createTestServer } from '../../../../test/ssr-server.ts'

// The server starts outside the test clock; the test measures only the render.
const vite = await createTestServer({ after })
const { render } = await vite.ssrLoadModule('svelte/server')
const { default: Segment } = await vite.ssrLoadModule('/src/lib/settings/accounts/UsageSegment.svelte')

test('a usage bar puts its percent in a chip inside the bar, with the pace word in its tooltip', () => {
  const window = { label: 'Weekly', usedPercent: 35, period: 'week', resetsAt: new Date(Date.now() + 3.5 * 86_400_000).toISOString() }
  const html = render(Segment, { props: { name: 'Work', window, observedAt: new Date().toISOString(), provider: 'anthropic' } }).body
  assert.match(html, /title="65% left, (ahead of pace|on pace|under pace)"[^>]*>65%.*?<span class="pace[^"]*"[^>]*>· (ahead of pace|on pace|under pace)</)
})
