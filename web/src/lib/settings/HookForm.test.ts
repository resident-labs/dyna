import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'
import { hookForm, hookOf, hookPatch, newHook, type HookFields } from './hooks.ts'
import { errorText } from '../errorText.ts'
import type { Hook } from '../api/settings.ts'

// Render the real form on the server and read its fields. The bundle is built once for the file.
const dir = await mkdtemp(join(tmpdir(), 'dyna-hook-form-'))
const result = await build({
  bundle: true, write: false,
  stdin: { contents: "export { default } from './HookForm.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
  format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
  plugins: [svelteEsbuildPlugin('server')],
})
const file = join(dir, 'HookForm.mjs')
await writeFile(file, result.outputFiles?.[0]?.contents ?? '')
const { default: component, render } = await import(pathToFileURL(file).href)
after(() => rm(dir, { recursive: true, force: true }))

test('the hook form shows the fields each event needs', () => {
  const show = (form: HookFields, editing = false): string => render(component, { props: { form, editing } }).body
  const add = show(newHook())
  assert.match(add, /aria-label="Add hook"/)
  assert.match(add, /id="hk-fail"/) // before-land can block
  assert.doesNotMatch(add, /id="hk-every"/)
  assert.match(add, /type="submit"[^>]*disabled/) // no name or command yet
  const sched = show({ ...newHook(), event: 'schedule' })
  assert.match(sched, /id="hk-every"/)
  assert.doesNotMatch(sched, /id="hk-fail"/)
  // Editing keeps the name; an old event name still shows as chosen.
  const edit = show(hookForm({ name: 'self-update', event: 'main-moved', command: 'x', timeout: '5m0s' }), true)
  assert.match(edit, /id="hk-name"[^>]*disabled/)
  assert.match(edit, /<option value="main-moved"[^>]*>Main updated/)
  assert.match(edit, /value="5m"/)
})

// Run the pane's real save handler with a fake api.
const pane = readFileSync(new URL('./HooksPane.svelte', import.meta.url), 'utf8')
interface Editor {
  hook: Partial<Hook> | null
  form: HookFields
  busy?: boolean
  error?: string
}
interface SaveContext {
  repo: string
  editor: Editor | null
  notes: string[]
  loads: number
  [name: string]: unknown
}
function paneWith(api: Record<string, unknown>, editor: Editor): SaveContext {
  const context: SaveContext = { api, errorText, repo: '/repo', editor, hookOf, hookPatch, notes: [], loads: 0 }
  context.say = (t: string) => context.notes.push(t)
  context.load = async () => { context.loads++ }
  runFunctions(pane, ['saveHook'], context)
  return context
}

test('saving adds a new hook, sends only changed fields, and keeps the form on a host error', async () => {
  const calls: unknown[][] = []
  const api = { addHook: async (...a: unknown[]) => calls.push(['add', ...a]), editHook: async (...a: unknown[]) => calls.push(['edit', ...a]) }
  const add = paneWith(api, { hook: null, form: { ...newHook(), name: 'verify', command: 'go test ./...' } })
  await call(add, 'saveHook')
  assert.deepEqual(calls[0], ['add', '/repo', { name: 'verify', event: 'before-land', command: 'go test ./...', cwd: 'worktree', timeout: '5m', onFail: 'alert' }])
  assert.equal(add.editor, null)
  assert.deepEqual(add.notes, ['verify was added.'])
  assert.equal(add.loads, 1)

  const stored = { name: 'verify', event: 'before-land', command: 'go test ./...', cwd: 'worktree', timeout: '5m0s', onFail: 'alert' }
  const same = paneWith(api, { hook: stored, form: hookForm(stored) })
  await call(same, 'saveHook')
  assert.equal(calls.length, 1) // nothing changed, nothing sent
  const edit = paneWith(api, { hook: stored, form: { ...hookForm(stored), command: 'make check' } })
  await call(edit, 'saveHook')
  assert.deepEqual(calls[1], ['edit', '/repo', 'verify', { command: 'make check' }])

  const failing = paneWith({ addHook: async () => { throw new Error('hook "verify" already exists') } }, { hook: null, form: { ...newHook(), name: 'verify', command: 'x' } })
  await call(failing, 'saveHook')
  assert.equal(need(failing.editor).busy, false)
  assert.match(need(failing.editor).error ?? '', /already exists/)
  assert.equal(failing.loads, 0)
})

// The pane state that the remove and repo-switch handlers read and write.
interface RemoveContext {
  repo: string
  hooks: Pick<Hook, 'name'>[] | null
  editor: Editor | null
  removing: { repo: string; name: string } | null
  [name: string]: unknown
}
const removeNames = ['load', 'loadRuns', 'loadLog', 'remove', 'pickRepo', 'say']
function removeContext(api: Record<string, unknown>, fields: Pick<RemoveContext, 'repo' | 'hooks' | 'editor' | 'removing'>): RemoveContext {
  const context: RemoveContext = { api, errorText, runs: {}, logs: {}, open: '', openRun: '', error: '', note: '', newestFirst: (x: unknown) => x, setTimeout, ...fields }
  runFunctions(pane, removeNames, context)
  return context
}

test('removing another hook keeps the open form and its unsaved input', async () => {
  let list = [{ name: 'a', event: 'after-land', command: 'x' }, { name: 'b', event: 'after-land', command: 'y' }]
  const api = { listHooks: async () => list, hookRuns: async () => [], removeHook: async (_: string, name: string) => { list = list.filter((h) => h.name !== name) } }
  const c = removeContext(api, { repo: '/repo', hooks: null, editor: { hook: list[0] ?? null, form: { ...hookForm(need(list[0])), command: 'unsaved edit' } }, removing: { repo: '/repo', name: 'b' } })
  await call(c, 'remove')
  assert.deepEqual(c.hooks?.map((h) => h.name), ['a'])
  assert.equal(need(c.editor).form.command, 'unsaved edit')
  // Picking another repo does close the form.
  await call(c, 'pickRepo')
  assert.equal(c.editor, null)
})

test('a pending remove belongs to its repo and is dropped when the repo changes', async () => {
  const removed: string[][] = []
  const api = { listHooks: async () => [], hookRuns: async () => [], removeHook: async (repo: string, name: string) => { removed.push([repo, name]) } }
  const c = removeContext(api, { repo: '/a', hooks: [], editor: null, removing: { repo: '/a', name: 'verify' } })
  // Switching repo while the confirm is armed drops it.
  c.repo = '/b'
  await call(c, 'pickRepo')
  assert.equal(c.removing, null)
  // A remove already in flight for /a cannot change a newer confirm on /b.
  c.repo = '/a'
  c.removing = { repo: '/a', name: 'verify' }
  const pending = call(c, 'remove')
  c.removing = { repo: '/b', name: 'verify' }
  await pending
  assert.deepEqual(removed, [['/a', 'verify']])
  assert.deepEqual(JSON.parse(JSON.stringify(c.removing)), { repo: '/b', name: 'verify' })
})
