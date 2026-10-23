import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { installDom, domNames } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'
import type { IssueEvent } from '../rooms/events.gen.ts'

// The issue card is one affordance for every filer, and it owns its actions:
// assign an issue to a goal, and close it with a reason. The actions call the
// engine, which posts the typed issue/updated or issue/closed event.
const rooms = fileURLToPath(new URL('.', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-issue-card-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})

const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  loader: { '.svg': 'dataurl' },
  stdin: {
    contents: "export { default as IssueCard } from './IssueCard.svelte';\nexport { mount, unmount, flushSync } from 'svelte';",
    resolveDir: rooms,
    loader: 'js',
  },
  plugins: [svelteEsbuildPlugin()],
})
const app = join(dir, 'issue.mjs')
await writeFile(app, need(built.outputFiles?.[0]).contents)
const web = await import(pathToFileURL(app).href)

interface Call {
  path: string
  body: unknown
}

// The transport posts JSON to /api/<Name>; a stub keeps the test offline and
// records what each card action asked the engine to do.
function stubFetch(calls: Call[]): void {
  globalThis.fetch = (async (path: unknown, init?: { body?: unknown }) => {
    calls.push({ path: String(path), body: init?.body ? JSON.parse(String(init.body)) : undefined })
    return { ok: true, status: 200, json: async () => ({}), text: async () => '', headers: new Headers() }
  }) as typeof fetch
}

const filed: IssueEvent = {
  type: 'filed',
  number: 3,
  title: 'Two retry loops in the agent runner',
  files: [{ path: 'internal/agents/run.go', line: 42 }],
  symbols: ['Runner.retry'],
  cost: 'Two retry paths drift apart.',
  direction: 'Keep one retry loop.',
  state: 'open',
  filer: 'dyna:librarian',
  detail: 'The librarian note.',
}

const room = { id: 'room_1', name: 'dyna' }

function render(payload: IssueEvent) {
  const post = {
    id: 'msg_0123456789abcdef', ts: 1, roomId: 'room_1', from: 'dyna', to: ['dyna'],
    kind: 'issue', notice: { kind: payload.type }, payload, text: 'the agent text',
  }
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.IssueCard, { target, props: { post, notice: post.notice, surface: 'feed', room } })
  web.flushSync()
  return { target, mounted }
}

const textOf = (node: Element): string => (node.textContent ?? '').replace(/\s+/g, ' ').trim()

test('the card shows the D-N number, the title, the files and who filed it', () => {
  const { target, mounted } = render(filed)
  const text = textOf(target)
  assert.match(text, /D-3/)
  assert.match(text, /Two retry loops in the agent runner/)
  assert.match(text, /internal\/agents\/run\.go:42/)
  assert.match(text, /filed by librarian/)
  assert.match(text, /open/)
  web.unmount(mounted)
  target.remove()
})

test('Assign gives the issue to a goal through the engine', async () => {
  const calls: Call[] = []
  stubFetch(calls)
  const { target, mounted } = render(filed)
  const assign = [...target.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Assign')
  need(assign, 'the Assign button').click()
  await new Promise((resolve) => setTimeout(resolve, 0))
  web.flushSync()
  assert.deepEqual(calls, [{ path: '/api/IssueGoal', body: ['room_1', 'msg_0123456789abcdef', ''] }])
  assert.match(textOf(target), /Assigned/)
  web.unmount(mounted)
  target.remove()
})

test('Close asks for a reason and sends it', async () => {
  const calls: Call[] = []
  stubFetch(calls)
  const { target, mounted } = render(filed)
  const close = [...target.querySelectorAll('button')].find((button) => button.textContent?.trim() === 'Close')
  need(close, 'the Close button').click()
  web.flushSync()
  const input = need(target.querySelector<HTMLInputElement>('input'), 'the reason field')
  input.value = 'Fixed by PR #542'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  web.flushSync()
  const submit = need(target.querySelector<HTMLButtonElement>('form button[type="submit"]'), 'the close submit button')
  submit.click()
  await new Promise((resolve) => setTimeout(resolve, 0))
  web.flushSync()
  assert.deepEqual(calls, [{ path: '/api/CloseIssue', body: ['room_1', 'msg_0123456789abcdef', 'Fixed by PR #542'] }])
  web.unmount(mounted)
  target.remove()
})

test('a closed issue offers no actions and names what closed it', () => {
  const { target, mounted } = render({ ...filed, type: 'closed', issueId: 'msg_0123456789abcdef', state: 'closed', actor: 'dyna:root', reason: 'Fixed by PR #542', closedBy: { pr: 542, commit: '24c91eb0' } })
  const labels = [...target.querySelectorAll('button')].map((button) => button.textContent?.trim())
  assert.ok(!labels.includes('Assign') && !labels.includes('Close') && !labels.includes('Open'), `a closed issue offers actions: ${labels.join(', ')}`)
  assert.match(textOf(target), /closed/)
  assert.match(textOf(target), /Fixed by PR #542/)
  assert.match(textOf(target), /Closed by PR #542/)
  web.unmount(mounted)
  target.remove()
})
