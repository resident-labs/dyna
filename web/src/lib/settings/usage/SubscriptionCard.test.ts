import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { createTestServer } from '../../../../test/ssr-server.ts'
import { need } from '../../../../test/need.ts'
import { usageGroups } from './state.ts'

// The server starts outside the test clock; each test measures only its render.
const vite = await createTestServer({ after })
const { render } = await vite.ssrLoadModule('svelte/server')
const { default: Card } = await vite.ssrLoadModule('/src/lib/settings/usage/SubscriptionCard.svelte')

test('usage cards name the harness above the account in both layouts, without a dashboard link', () => {
  const accounts: [string, string, string, { id: string; label: string }?][] = [
    ['grok', 'Grok', 'Current Grok account', { id: 'x-subscription', label: 'X' }],
    ['grok', 'Grok', 'Legacy account', { id: 'grok-legacy', label: 'Legacy' }],
    ['claude-code', 'Claude Code', '3'],
    ['claude-code', 'Claude Code', 'Work account'],
    ['codex', 'Codex', 'ChatGPT account'],
    ['muse', 'Muse', 'Muse account'],
  ]
  for (const compact of [false, true]) {
    for (const [harness, name, label, plan] of accounts) {
      const group = need(usageGroups([{ id: 'account', harness, label, plan }])[0])
      const html = render(Card, { props: { group, compact } }).body
      assert.match(html, new RegExp(`<h4[^>]*>${name}(?:<[^>]*>)*</h4>`), `${harness} heading, compact=${compact}`)
      assert.match(html, new RegExp(`<b[^>]*>${label}</b>`))
      assert.doesNotMatch(html, /grok\.com\/dashboard/)
    }
  }
})

test('OpenCode cards show Active without an unknown-plan placeholder or leading separator', () => {
  const group = need(usageGroups([{ id: 'account', harness: 'opencode', integration: 'opencode-go', active: true, status: 'ready' }])[0])
  const html = render(Card, { props: { group } }).body
  assert.doesNotMatch(html, /Plan not reported|· Active/)
  assert.match(html, /<p[^>]*>Active<\/p>/)
})
