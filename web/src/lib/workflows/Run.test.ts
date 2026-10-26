import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import type { RunAgent, RunSummary } from '../api/workflow.ts'
import { createTestServer } from '../../../test/ssr-server.ts'

test('a running worker shows its elapsed time and what it does now', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const detail = {
    run: { id: 'wf_a', name: 'research', status: 'running', startedAt: new Date().toISOString() },
    agents: [{ id: 2, label: 'T3', status: 'running', started: Date.now() - 125000, doing: { text: 'webfetch: https://x.dev' } }],
    agentTotal: 1, agentRunning: 1, agentFailed: 0, logs: [], phases: [],
  }
  const body = render(Run, { props: { detail } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /2m 0[5-6]s/)
  assert.match(body, /<p class="line live[^"]*">webfetch: https:\/\/x\.dev<\/p>/)
})

test('a running run offers Pause, and a paused one says so and offers Resume', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const detail = (run: Partial<RunSummary>) => ({ run: { id: 'wf_a', name: 'build', status: 'running', startedAt: new Date().toISOString(), ...run }, agents: [], agentTotal: 0, agentRunning: 0, agentFailed: 0, logs: [], phases: [] })
  const html = (run: Partial<RunSummary>, props: object = { onpause: async () => {} }) => render(Run, { props: { detail: detail(run), ...props } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(html({}), /<button class="pause[^"]*"[^>]*>Pause<\/button>/)
  const paused = html({ paused: true })
  assert.match(paused, /<button class="pause[^"]*"[^>]*>Resume<\/button>/)
  assert.match(paused, />Paused<\/span>/)
  assert.match(paused, /no new agents start until you resume it/)
  assert.doesNotMatch(html({ status: 'ok' }), /class="pause/, 'a finished run has no pause control')
  assert.doesNotMatch(html({}, {}), /class="pause/, 'no control without a handler')
})

// The component's own declarations for one selector.
function ruleOf(css: string, selector: string) {
  const plain = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const found = [...plain.matchAll(/([^{}]+)\{([^}]*)\}/g)].find((m) => m[1]?.trim() === selector)
  return found ? (found[2] ?? '').replace(/\s+/g, ' ').trim() : ''
}

// An agent group used to be a clipping flex column, so a row that outgrew its
// box was cut at the card edge and its text overlapped the card. Every state
// must therefore keep its whole row: the group never clips and never flex-sizes
// a row, and a completed row draws its name and time with or without a preview.
test('a group keeps every completed row whole, with or without a preview', async (t) => {
  const source = await readFile(new URL('./Run.svelte', import.meta.url), 'utf8')
  const css = source.slice(source.lastIndexOf('<style>') + 7, source.lastIndexOf('</style>'))
  const group = ruleOf(css, '.agents')
  assert.ok(group, 'the group has its own rule')
  assert.doesNotMatch(group, /overflow:\s*hidden/, 'the group must not clip a row')
  assert.doesNotMatch(group, /display:\s*flex/, 'the group must not flex-size a row')
  assert.doesNotMatch(ruleOf(css, '.agent'), /(^|;|\s)height:/, 'a row has no fixed height')

  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const card = (agent: Partial<RunAgent>) => render(Run, {
    props: {
      detail: {
        run: { id: 'wf_a', name: 'research', status: 'ok', startedAt: new Date().toISOString() },
        agents: [agent], agentTotal: 1, agentRunning: 0, agentFailed: 0, logs: [], phases: [],
      },
    },
  }).body.replace(/<!--[\s\S]*?-->/g, '')
  const done = card({ id: 1, label: 'handback-a', status: 'ok', durMs: 163337, preview: '6 prs: Adds a hook.' })
  assert.match(done, /<span class="label[^"]*">handback-a<\/span>/)
  assert.match(done, /2m 43s/)
  assert.match(done, /<p class="line[^"]*">6 prs: Adds a hook.<\/p>/)
  const quiet = card({ id: 2, label: 'handback-b', status: 'ok', durMs: 191713 })
  assert.match(quiet, /<span class="label[^"]*">handback-b<\/span>/)
  assert.match(quiet, /3m 12s/)
  assert.doesNotMatch(quiet, /class="line/)
  const failed = card({ id: 3, label: 'handback-c', status: 'error', durMs: 5000, error: 'the worker exited before it wrote a result' })
  assert.match(failed, /<span class="label[^"]*">handback-c<\/span>/)
  assert.match(failed, /<p class="line[^"]*bad[^"]*">the worker exited before it wrote a result<\/p>/)
})

// The chevron button is styled by `.open` (22 px square). An expanded row used to
// carry the same class, so the row shrank to 28 px and its text overflowed on a
// phone. The row marks itself `expanded`, and its text column may shrink.
test('an expanded agent row does not share the chevron button class', async () => {
  const source = await readFile(new URL('./Run.svelte', import.meta.url), 'utf8')
  const css = source.slice(source.lastIndexOf('<style>') + 7, source.lastIndexOf('</style>'))
  assert.doesNotMatch(source, /class:open=/)
  assert.match(source, /class:expanded=\{row\?\.open\}/)
  assert.match(ruleOf(css, '.agent'), /grid-template-columns: 18px minmax\(0, 1fr\) auto/)
})
