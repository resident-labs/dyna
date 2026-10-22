import test from 'node:test'
import assert from 'node:assert/strict'
import { isRecord } from '../api/guards.ts'
import { createTestServer } from '../../../test/ssr-server.ts'
import { renderedBody } from '../../../test/svelte-render.ts'
import { call } from '../../../test/svelte-source.ts'

test('JsonView typesets fields, records, links, lists and paragraphs', async (t) => {
  const vite = await createTestServer(t)
  const serverModule: unknown = await vite.ssrLoadModule('svelte/server')
  const viewModule: unknown = await vite.ssrLoadModule('/src/lib/ui/JsonView.svelte')
  if (!isRecord(serverModule)) assert.fail('svelte/server did not load')
  if (!isRecord(viewModule) || !('default' in viewModule)) assert.fail('JsonView has no default export')
  const value = {
    results: [{ tool: 'Tern', summary: 'A Mac app.', sources: [{ title: 'Docs', url: 'https://tern.build/docs' }] }, { tool: 'T3', summary: 'Free.' }],
    note: 'First part with `code`.\n\nSecond part.',
    tags: ['a', 'b'],
    confidence: 'high',
  }
  const html = (props: { value: unknown }): string => renderedBody(call(serverModule, 'render', viewModule.default, { props })).replace(/<!--[\s\S]*?-->/g, '')
  const body = html({ value })
  assert.match(body, /<dt[^>]*>Results<\/dt>/)
  assert.match(body, /<summary[^>]*>[\s\S]*Tern[\s\S]*A Mac app\.[\s\S]*<\/summary>/)
  assert.doesNotMatch(body, /Sources/, 'closed records render no body')
  assert.match(body, /<p[^>]*>First part with <code[^>]*>code<\/code>\.<\/p>\s*<p[^>]*>Second part\.<\/p>/)
  assert.match(body, /<li><span[^>]*>a<\/span>\s*<\/li><li><span[^>]*>b<\/span>/)
  const link = html({ value: { title: 'Docs', url: 'https://tern.build/docs' } })
  assert.match(link, /<a href="https:\/\/tern\.build\/docs" target="_blank" rel="noopener noreferrer"[^>]*>Docs<\/a>/)
  const evil = html({ value: { note: '<script>x</script>' } })
  assert.doesNotMatch(evil, /<script>x/)
})

test('one field holding a collection shows as a caption, not a label column', async (t) => {
  const vite = await createTestServer(t)
  const serverModule: unknown = await vite.ssrLoadModule('svelte/server')
  const viewModule: unknown = await vite.ssrLoadModule('/src/lib/ui/JsonView.svelte')
  if (!isRecord(serverModule)) assert.fail('svelte/server did not load')
  if (!isRecord(viewModule) || !('default' in viewModule)) assert.fail('JsonView has no default export')
  const body = renderedBody(call(serverModule, 'render', viewModule.default, { props: { value: { results: [{ tool: 'Tern' }, { tool: 'T3' }] } } })).replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /<p class="caption[^"]*">Results · 2<\/p>/)
  assert.doesNotMatch(body, /<dl/)
})
