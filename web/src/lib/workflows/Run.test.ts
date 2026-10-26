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

test('phases that share stages show a lanes grid and a failed agent in the attention strip', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const agent = (id: number, phase: string, status: string, label = `T${id}`): RunAgent => ({ id, label, phase, status })
  const detail = {
    run: { id: 'wf_a', name: 'continuous', status: 'running', startedAt: new Date().toISOString() },
    agents: [agent(1, 'CH prepare', 'ok'), agent(2, 'BS prepare', 'ok'), agent(3, 'BS review', 'error', 'reviewer'), agent(4, 'CH review', 'running')],
    agentTotal: 4, agentRunning: 1, agentFailed: 1, logs: [], phases: [],
  }
  const body = render(Run, { props: { detail } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /aria-label="Progress by lane and stage"/)
  assert.match(body, /reviewer failed in BS review/)
  assert.match(body, /Tap a cell to see its steps/)
  assert.doesNotMatch(body, /class="agent /, 'agents stay folded until a cell is opened')
})

test('phases without shared stages show one row each, with agents folded', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const agent = (id: number, phase: string): RunAgent => ({ id, label: `T${id}`, phase, status: id === 3 ? 'running' : 'ok' })
  const detail = {
    run: { id: 'wf_a', name: 'research', status: 'running', startedAt: new Date().toISOString() },
    agents: [agent(1, 'Plan'), agent(2, 'Build'), agent(3, 'Build')],
    agentTotal: 3, agentRunning: 1, agentFailed: 0, logs: [], phases: [],
  }
  const body = render(Run, { props: { detail } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /aria-label="Phases"/)
  assert.match(body, /class="name[^"]*">Build<\/span>[\s\S]*?1\/2/)
  assert.doesNotMatch(body, /class="agent /, 'agents stay folded under their row')
  assert.doesNotMatch(body, /class="steps/, 'the chip chain is gone')
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

test('an interrupted run is labeled Interrupted and shows its error', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const run: Partial<RunSummary> = { id: 'wf_a', name: 'build', status: 'interrupted', startedAt: new Date().toISOString(), error: 'interrupted by update from v1 to v2' }
  const detail = { run, agents: [], agentTotal: 0, agentRunning: 0, agentFailed: 0, logs: [], phases: [] }
  const body = render(Run, { props: { detail } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, />Interrupted<\/span>/)
  assert.match(body, /interrupted by update from v1 to v2/)
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

test('a running agent card is one button that folds it; Session is not in the closed header', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const detail = {
    run: { id: 'wf_a', name: 'research', status: 'running', startedAt: new Date().toISOString() },
    agents: [{ id: 1, label: 'new', status: 'running', session: 'ses_20261026-000000-aaaa' }, { id: 2, label: 'bad', status: 'error', error: 'died' }],
    agentTotal: 2, agentRunning: 1, agentFailed: 1, logs: [], phases: [],
  }
  const body = render(Run, { props: { detail, onopen: () => {} } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /<button type="button" class="head action[^"]*" aria-expanded="false" aria-label="Show the journal of new">/)
  assert.doesNotMatch(body, /aria-label="[^"]*of bad"/, 'a failed agent has no result to open')
  assert.doesNotMatch(body, /<button class="session[^"]*"[^>]*>Session</, 'the Session button moved into the open body')
})

// run() commands share the run view with agents: they place in the lanes grid
// like agents, and the grid marks their cells; their cards read as commands.
test('commands place in lane cells, which mark them, and failures draw attention', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const detail = {
    run: { id: 'wf_a', name: 'bridge', status: 'running', startedAt: new Date().toISOString() },
    agents: [
      { id: 1, label: 'scan', phase: 'CH prepare', status: 'ok', started: 1000 },
      { id: 2, label: 'rev', phase: 'CH review', status: 'ok', started: 4000 },
    ],
    commands: [
      { id: 1, label: 'make', phase: 'CH prepare', status: 'ok', started: 2000, command: 'make test', durMs: 2340, exitCode: 0, tail: 'PASS\nok' },
      { id: 2, label: 'up', phase: 'BS prepare', status: 'error', started: 3000, command: 'up --unit KG', durMs: 500, exitCode: 3, error: 'exit status 3' },
    ],
    agentTotal: 2, agentRunning: 0, agentFailed: 0,
    commandTotal: 2, commandRunning: 0, commandFailed: 1, logs: [], phases: [],
  }
  const body = render(Run, { props: { detail } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /aria-label="CH prepare: 2 of 2 done, 1 command"/)
  assert.match(body, /aria-label="BS prepare: 1 of 1 done, 1 command"/)
  assert.match(body, /title="run\(\) commands">\$1<\/span>/)
  assert.match(body, /Commands<\/dt><dd[^>]*>2<\/dd>/)
  assert.match(body, /up failed in BS prepare/)
  assert.doesNotMatch(body, /class="agent /, 'cells stay folded until picked')
})

test('a command card shows the command line, exit code and log tail', async (t) => {
  const vite = await createTestServer(t)
  const { render } = await vite.ssrLoadModule('svelte/server')
  const { default: Run } = await vite.ssrLoadModule('/src/lib/workflows/Run.svelte')
  const detail = {
    run: { id: 'wf_b', name: 'bridge', status: 'ok', startedAt: new Date().toISOString() },
    agents: [{ id: 7, label: 'scan', phase: 'Build', status: 'ok', started: 1000, durMs: 9000 }],
    commands: [
      { id: 1, label: 'make', phase: 'Build', status: 'ok', started: 2000, command: 'make test', durMs: 2340, exitCode: 0, tail: 'PASS\nok' },
      { id: 2, label: 'up', phase: 'Build', status: 'error', started: 3000, command: 'up --unit KG', durMs: 500, exitCode: 3, error: 'exit status 3' },
    ],
    agentTotal: 1, agentRunning: 0, agentFailed: 0,
    commandTotal: 2, commandRunning: 0, commandFailed: 1, logs: [], phases: [],
  }
  const body = render(Run, { props: { detail } }).body.replace(/<!--[\s\S]*?-->/g, '')
  assert.match(body, /1 agent · 2 commands/)
  assert.match(body, /class="agent ok command[^"]*"/)
  assert.match(body, /class="agent error command[^"]*"/)
  assert.match(body, /<span class="pill[^"]*">\$<\/span>/, 'a command card carries the $ marker, not a profile')
  assert.doesNotMatch(body, /<span class="pill">sonnet<\/span>/)
  assert.match(body, /<p class="line mono cmdline[^"]*">\$ make test<\/p>/)
  assert.match(body, /<span class="pill[^"]*">exit 0<\/span>/)
  assert.match(body, /<span class="pill[^"]* bad">exit 3<\/span>/)
  assert.match(body, /<p class="line bad[^"]*">exit status 3<\/p>/)
  assert.match(body, /<pre class="tail mono[^"]*">PASS\nok<\/pre>/)
  assert.doesNotMatch(body, /aria-label="Show the (result|journal) of (make|up)"/, 'a command has no worker result to open')
})
