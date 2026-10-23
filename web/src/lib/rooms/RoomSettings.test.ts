import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import vm from 'node:vm'
import { compile, compileModule, parse } from 'svelte/compiler'
import { build } from 'esbuild'
import { normalizePolicy, budgetForm, checksText, saveRoomPolicy, DEFAULT_POLICY } from '../prs/pr.ts'
import { stripTypes, topLevelFunctions } from '../../../test/svelte-source.ts'
import type { Action } from '../ui/actions.ts'

// Render the real Room settings modal on the server and read the text. The
// ServiceHeader is a stub that prints its role, so each section shows which
// services it holds.
async function load() {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-room-settings-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: "export { default } from './RoomSettings.svelte'; export { render } from 'svelte/server'; export { createRawSnippet } from 'svelte'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /[/\\]ServiceHeader\.svelte$/ }, ({ path }) => ({ contents: compile('<script>let { roomId, role } = $props()</script><b>service {role} in {roomId}</b>', { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte\.js$/ }, ({ path }) => ({ contents: compileModule(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /\.svelte$/ }, ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]api\.ts$/ }, () => ({ contents: 'export const api = {}; export const openExternal = () => {}; export const features = {}', loader: 'js' }))
    } }],
  })
  const output = result.outputFiles?.[0]
  assert.ok(output, 'the room settings modal built')
  const file = join(dir, 'RoomSettings.mjs')
  await writeFile(file, output.contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render, createRawSnippet: module.createRawSnippet }
}
const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')
const room = { id: 'room-1', name: 'dyna-agent', displayName: 'Dyna', repo: '/repo', pinned: true }

test('the modal uses the Settings shell with the room sections in order', async () => {
  const { dir, component, render, createRawSnippet } = await load()
  try {
    const rules = createRawSnippet(() => ({ render: () => '<p>the rules list</p>' }))
    const html = render(component, { props: { room, rules, onclose() {} } }).body
    assert.match(html, /class="shell[^"]*" role="dialog" aria-modal="true" aria-label="Dyna settings"/)
    assert.match(text(html), /^ Dyna settings General Review &amp; landing CI time Services Rules Hooks Bot noise rules General /)
    assert.match(html, /aria-label="Close dyna settings"/)
    assert.match(text(render(component, { props: { room, rules, start: 'rules', onclose() {} } }).body), /Bot noise rules the rules list/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

test('each section shows its moved controls in one shared layout', async () => {
  const { dir, component, render } = await load()
  try {
    const show = (start: string, props: Record<string, unknown> = {}) => render(component, { props: { room, start, onclose() {}, ...props } }).body
    const general = show('general', { controls: { state: { paused: true }, busy: false } })
    assert.match(text(general), /General .*Name Agents still address the room as #dyna-agent\. Dyna Rename… Pin to rail .*Pause agents Paused\./)
    assert.match(text(general), /Danger zone Archive .*Archive room Delete .*Delete room…/)
    assert.equal((general.match(/role="switch" aria-checked="true"/g) || []).length, 2)
    // No pause state yet: the switch waits for the host.
    assert.match(show('general'), /aria-label="Pause agents" disabled/)

    const review = text(show('review', { reviewer: { enabled: true } }))
    assert.match(review, /Review &amp; landing .*Automatic review Review new PRs .*Reviewer sessions .*Restart reviewer/)
    assert.doesNotMatch(review, /Review starts after/) // the room policy loads after mount
    assert.doesNotMatch(text(show('review')), /Automatic review/)

    assert.match(text(show('services')), /service reviewer in room-1 service keeper in room-1/)
    // The room's repo is fixed, so the hooks list has no repo picker.
    const hooks = show('hooks')
    assert.match(text(hooks), /Hooks Shell commands/)
    assert.match(text(hooks), /Shared by every room on this repository\./)
    assert.doesNotMatch(hooks, /aria-label="Repo"/)
    assert.match(text(show('hooks', { room: { ...room, repo: '' } })), /This room has no repository\./)
    assert.match(text(show('noise')), /Bot noise rules .*Learned bot noise rules/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

// Run the page's real handlers with a fake api.
const source = readFileSync(new URL('./RoomSettings.svelte', import.meta.url), 'utf8')
const handlers = (names: readonly string[]) => (parse(source, { modern: true }).instance?.content.body ?? []).filter((node) =>
  (node.type === 'FunctionDeclaration' && names.includes(node.id?.name ?? '')) ||
  (node.type === 'VariableDeclaration' && node.declarations[0]?.id.type === 'Identifier' && names.includes(node.declarations[0].id.name))
).map((node) => {
  const { start, end } = nodeRange(node)
  return source.slice(start, end)
}).join('\n')

type Node = { start: number; end: number }
const group = (match: RegExpExecArray, index: number): string => {
  const value = match[index]
  assert.ok(value, `group ${index}`)
  return value
}
function nodeRange(node: object): Node {
  if (typeof Reflect.get(node, 'start') !== 'number' || typeof Reflect.get(node, 'end') !== 'number') throw new Error('the node has no source range')
  return { start: Reflect.get(node, 'start') as number, end: Reflect.get(node, 'end') as number }
}

interface PageFns {
  load(): Promise<void>
  save(): Promise<void>
  loadPublishing(): Promise<void>
  savePublishing(): Promise<void>
  fill(): void
  formOf(...args: unknown[]): unknown
  policyRequest(...args: unknown[]): unknown
  policyRoom(...args: unknown[]): unknown
  policyLoading(...args: unknown[]): unknown
  saveAfterPolicy(...args: unknown[]): unknown
  reviewerBusy(...args: unknown[]): unknown
}

interface PageState {
  roomId: string
  policy: unknown
  form: Record<string, unknown> | null
  busy: boolean
  dirty: boolean
  error: string
  saved: boolean
  reviewPublishing: string
  githubAccess: string
  mergeMaxParallel: number
  publishError: string
}

function page<K extends keyof PageFns>(api: object, names: readonly K[], extra: Record<string, unknown> = {}): PageState & Pick<PageFns, K> {
  const state: PageState = { roomId: 'r', policy: null, form: null, busy: false, dirty: true, error: '', saved: false, reviewPublishing: 'local', githubAccess: 'read-only', mergeMaxParallel: 3, publishError: '' }
  const context = Object.assign({ api, $state: (value: unknown) => value, normalizePolicy, budgetForm, checksText, saveRoomPolicy, onreviewer: async () => {}, prSettings: null, ...extra }, state)
  vm.createContext(context)
  vm.runInContext(stripTypes(handlers([...names, 'formOf', 'policyRequest', 'policyRoom', 'policyLoading', 'saveAfterPolicy', 'reviewerBusy'])), context)
  return Object.assign(context, vm.runInContext(`({ ${names.join(', ')} })`, context) as Pick<PageFns, K>)
}

test('the policy saves all budget fields, reloads them, and reports rejected writes', async () => {
  let stored: Record<string, unknown> = { ...DEFAULT_POLICY, ciTestAllowNotes: { 'pkg.Test/a,b': 'External service' } }
  let fail = false
  const api = { roomChangePolicy: async () => stored, setRoomChangePolicy: async (_room: string, next: unknown) => {
    if (fail) throw new Error('CI budget requires 0 < warn < fail')
    stored = JSON.parse(JSON.stringify(next))
  } }
  const open = () => page(api, ['load', 'save', 'fill'] as const)
  const first = open()
  await first.load()
  assert.equal(first.form?.ciNoJUnitWarning, true)
  assert.ok(first.form)
  Object.assign(first.form, { ciWarnAfter: '4m', ciFailAfter: '5m', ciTestMax: '20s', ciTestAllow: 'pkg.Test/a,b\nSuite.test[x]', ciTestAllowNotes: { 'pkg.Test/a,b': '12 seconds: external service', 'Suite.test[x]': '15 seconds: setup' }, ciNoJUnitWarning: false, requiredChecks: 'one, two' })
  await first.save()
  assert.equal(first.saved, true)
  assert.equal(first.busy, false)
  const reopened = open()
  await reopened.load()
  assert.ok(reopened.form && first.form)
  for (const key of ['ciWarnAfter', 'ciFailAfter', 'ciTestMax', 'ciTestAllow', 'ciNoJUnitWarning']) assert.equal(reopened.form[key], first.form[key])
  assert.deepEqual(stored.requiredChecks, ['one', 'two'])
  assert.deepEqual(stored.ciTestAllowNotes, { 'pkg.Test/a,b': '12 seconds: external service', 'Suite.test[x]': '15 seconds: setup' })
  fail = true
  reopened.form.ciWarnAfter = '8m'
  await reopened.save()
  assert.match(reopened.error, /0 < warn < fail/)
  assert.equal(reopened.saved, false)
  assert.equal(reopened.busy, false)
  assert.equal(stored.ciWarnAfter, '4m')
})

test('where reviews appear: legacy settings read local, a choice persists and keeps old fields', async () => {
  let stored: Record<string, unknown> = { reviewerSession: 'reviewer' }
  const api = { prSettings: async () => stored, setPRSettings: async (_room: string, next: unknown) => { stored = JSON.parse(JSON.stringify(next)) } }

  const first = page(api, ['loadPublishing', 'savePublishing'] as const)
  await first.loadPublishing()
  assert.equal(first.reviewPublishing, 'local')
  first.reviewPublishing = 'github'
  await first.savePublishing()
  assert.deepEqual(stored, { reviewerProfile: '', reviewerSession: 'reviewer', reviewPublishing: 'github', githubAccess: 'read-only', mergeMaxParallel: 3 })
  const reopened = page(api, ['loadPublishing', 'savePublishing'] as const)
  await reopened.loadPublishing()
  assert.equal(reopened.reviewPublishing, 'github')
  assert.equal(reopened.githubAccess, 'read-only')
  reopened.githubAccess = 'read-write'
  await reopened.savePublishing()
  const accessReopened = page(api, ['loadPublishing', 'savePublishing'] as const)
  await accessReopened.loadPublishing()
  assert.equal(accessReopened.githubAccess, 'read-write')
})

test('a failed publishing write shows the error and returns the select to the stored value', async () => {
  const c = page({ prSettings: async () => ({}), setPRSettings: async () => { throw new Error('write failed') } }, ['loadPublishing', 'savePublishing'] as const)
  await c.loadPublishing()
  c.reviewPublishing = 'github'
  c.githubAccess = 'read-write'
  await c.savePublishing()
  assert.equal(c.publishError, 'write failed')
  assert.equal(c.reviewPublishing, 'local')
  assert.equal(c.githubAccess, 'read-only')
})

// App's real wiring: the room menu item runs openRoomSettings, App mounts
// the modal while showRoomSettings is true, and the shell's dialog action
// calls the modal's onclose on Esc.
test('the room menu opens the modal, for another room after selecting it, and Esc closes it', async () => {
  const app = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
  const opener = /openRoomSettings: (async \(target\) => \{.*\}),\n/.exec(app)?.[1]
  assert.ok(opener, 'App has openRoomSettings')
  const mount = /\{#if (showRoomSettings && room)\}\n(?:.*\n)*?\s*<RoomSettings [^\n]*onclose=\{(\(\) => \(showRoomSettings = false\))\} \/>/.exec(app)
  assert.ok(mount, 'App mounts RoomSettings with its close handler')
  const selected: string[] = []
  const state = vm.createContext({ roomId: 'room-1', room, showRoomSettings: false, roomSettingsAt: null, roomSettingsKey: 0, selectRoom: async (id: string) => { selected.push(id); state.roomId = id; return true } })
  const { actions, roomMenu } = await import('../ui/actions.ts')
  // The menu reads only the room and its settings opener.
  const openRoomSettings = vm.runInContext(stripTypes(opener), state)
  const item = (r: typeof room): Action | undefined => {
    const menuContext: object = { room: r, rooms: [r], icons: {}, openRoomSettings }
    return roomMenu(actions(menuContext as Parameters<typeof actions>[0])).find((a) => a.id === 'roomsettings')
  }

  await item(room)?.run()
  assert.ok(vm.runInContext(stripTypes(group(mount, 1)), state))
  assert.deepEqual(selected, [])

  const shell = readFileSync(new URL('../ui/SettingsShell.svelte', import.meta.url), 'utf8')
  assert.match(shell, /role="dialog" aria-modal="true"[^>]*use:modal=\{\{ close: onclose, busy \}\}/)
  const { dialog } = await import('../ui/dialog.ts')
  interface EscapeEvent {
    key: string
    target?: unknown
    stopped: boolean
    preventDefault(): void
    stopImmediatePropagation(): void
    stopPropagation(): void
  }
  const listeners = new Set<(event: EscapeEvent) => void>()
  Reflect.set(globalThis, 'window', { addEventListener: (_: string, fn: (event: EscapeEvent) => void) => listeners.add(fn), removeEventListener: (_: string, fn: (event: EscapeEvent) => void) => listeners.delete(fn) })
  Reflect.set(globalThis, 'document', { activeElement: null })
  // dialog reads only these members of its card, so the fake names them.
  const fake: object = { parentElement: { addEventListener() {}, removeEventListener() {} }, focus() {}, contains: () => false, querySelectorAll: () => [], querySelector: () => null }
  const node = fake as HTMLElement
  const action = dialog(node, { close: vm.runInContext(stripTypes(group(mount, 2)), state), busy: false })
  for (const fn of listeners) fn({ key: 'Escape', preventDefault() {}, stopImmediatePropagation() {}, stopPropagation() {}, stopped: false })
  assert.equal(state.showRoomSettings, false)
  action.destroy()
  Reflect.deleteProperty(globalThis, 'window')
  Reflect.deleteProperty(globalThis, 'document')

  await item({ ...room, id: 'room-2' })?.run()
  assert.deepEqual(selected, ['room-2'])
  assert.equal(state.showRoomSettings, true)
})

// The Rules section's real textarea handler and editing effect, inside the
// real dialog action with App's busy={rulesEditing} wiring.
test('Esc in a rule edit cancels the edit, and leaving Rules mid-edit frees Esc for the modal', async () => {
  const rulesSource = readFileSync(new URL('./Rules.svelte', import.meta.url), 'utf8')
  const effect = /\$effect\((\(\) => \{ editing = !!edit;.*?\})\)\n/.exec(rulesSource)?.[1]
  assert.ok(effect, 'Rules reacts to an open edit')
  const rules = vm.createContext({ edit: null as { id: string; text: string } | null, editGeneration: 0, editing: false, save() {}, tick: async () => {}, document: { getElementById: () => null } })
  const rulesHandlers = topLevelFunctions(rulesSource, ['editKey', 'cancelEdit', 'restoreEditFocus'])
  vm.runInContext([...rulesHandlers.values()].join('\n'), rules)
  interface EscapeEvent {
    key: string
    target?: unknown
    stopped: boolean
    preventDefault(): void
    stopImmediatePropagation(): void
    stopPropagation(): void
  }
  const textareaKey = vm.runInContext('editKey', rules) as (event: EscapeEvent) => void
  let cleanup: () => void = () => {}
  const runEffect = vm.runInContext(stripTypes(effect), rules) as () => () => void
  const rerun = () => { cleanup(); cleanup = runEffect() }

  const { dialog } = await import('../ui/dialog.ts')
  const listeners = new Set<(event: EscapeEvent) => void>()
  Reflect.set(globalThis, 'window', { addEventListener: (_: string, fn: (event: EscapeEvent) => void) => listeners.add(fn), removeEventListener: (_: string, fn: (event: EscapeEvent) => void) => listeners.delete(fn) })
  Reflect.set(globalThis, 'document', { activeElement: null })
  const textarea = {}
  // dialog reads only these members of its card, so the fake names them.
  const fake: object = { parentElement: { addEventListener() {}, removeEventListener() {} }, focus() {}, contains: (el: unknown) => el === textarea, querySelectorAll: () => [], querySelector: () => null }
  const node = fake as HTMLElement
  let closed = 0
  const action = dialog(node, { close: () => closed++, busy: false })
  // window capture first, then the target, as in the browser.
  function escape(target: unknown) {
    const event: EscapeEvent = { key: 'Escape', target, stopped: false, preventDefault() {}, stopImmediatePropagation() { event.stopped = true }, stopPropagation() { event.stopped = true } }
    for (const fn of listeners) fn(event)
    if (!event.stopped && target === textarea) textareaKey(event)
  }
  const sync = () => action.update({ close: () => closed++, busy: rules.editing })

  rules.edit = { id: 'r1', text: 'x' }; rerun(); sync()
  escape(textarea)
  assert.equal(rules.edit, null)
  assert.equal(closed, 0)
  rerun(); sync()
  assert.equal(rules.editing, false)

  rules.edit = { id: 'r1', text: 'x' }; rerun(); sync()
  assert.equal(rules.editing, true)
  cleanup(); sync() // select General: Rules unmounts
  assert.equal(rules.editing, false)
  escape(node)
  assert.equal(closed, 1)
  action.destroy()
  Reflect.deleteProperty(globalThis, 'window')
  Reflect.deleteProperty(globalThis, 'document')
})

// HooksPane's real onmodal effect with Room settings' real onmodal handler.
test('leaving Hooks with a hook editor open frees Esc for the modal', async () => {
  const hooksSource = readFileSync(new URL('../settings/HooksPane.svelte', import.meta.url), 'utf8')
  const effect = /\$effect\((\(\) => \{ onmodal\?\.\(!!editor\);.*?\})\)\n/.exec(hooksSource)?.[1]
  assert.ok(effect, 'HooksPane reacts to its editor')
  const handler = /<HooksPane [^>]*onmodal=\{(\(open\) => \(hooksBusy = open\))\}/.exec(source)?.[1]
  assert.ok(handler, 'RoomSettings wires the HooksPane modal state')
  const state = vm.createContext({ hooksBusy: false })
  const pane = vm.createContext({ editor: { form: {} }, onmodal: vm.runInContext(stripTypes(handler), state) })
  const cleanup = (vm.runInContext(stripTypes(effect), pane) as () => () => void)()
  assert.equal(state.hooksBusy, true) // Add hook: the editor is open

  const { dialog } = await import('../ui/dialog.ts')
  interface EscapeEvent {
    key: string
    target?: unknown
    preventDefault(): void
    stopImmediatePropagation(): void
  }
  const listeners = new Set<(event: EscapeEvent) => void>()
  Reflect.set(globalThis, 'window', { addEventListener: (_: string, fn: (event: EscapeEvent) => void) => listeners.add(fn), removeEventListener: (_: string, fn: (event: EscapeEvent) => void) => listeners.delete(fn) })
  Reflect.set(globalThis, 'document', { activeElement: null })
  // dialog reads only these members of its card, so the fake names them.
  const fake: object = { parentElement: { addEventListener() {}, removeEventListener() {} }, focus() {}, contains: (el: unknown) => el === node, querySelectorAll: () => [], querySelector: () => null }
  const node = fake as HTMLElement
  let closed = 0
  const action = dialog(node, { close: () => closed++, busy: state.hooksBusy })
  cleanup() // select General: HooksPane unmounts
  action.update({ close: () => closed++, busy: state.hooksBusy })
  for (const fn of listeners) fn({ key: 'Escape', target: node, preventDefault() {}, stopImmediatePropagation() {} })
  assert.equal(closed, 1)
  action.destroy()
  Reflect.deleteProperty(globalThis, 'window')
  Reflect.deleteProperty(globalThis, 'document')
})

test('room switches use the shared component and retain checked states', async () => {
  assert.doesNotMatch(source, /\.switch\b/)
  assert.match(source, /import Switch from '\.\.\/ui\/Switch\.svelte'/)
  const { dir, component, render } = await load()
  try {
    const html = render(component, { props: { room: { ...room, pinned: false }, controls: { state: { paused: true } }, onclose() {} } }).body
    assert.match(html, /role="switch" aria-checked="false" aria-label="Pin to rail"/)
    assert.match(html, /role="switch" aria-checked="true" aria-label="Pause agents"/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})


test('parallel prefix trials load, save, and restore after a failed write', async () => {
 let stored: Record<string, unknown> = { mergeMaxParallel: 2 }
 let fail = false
 const c = page({ prSettings: async () => stored, setPRSettings: async (_room: string, next: unknown) => { if (fail) throw new Error('write failed'); stored = JSON.parse(JSON.stringify(next)) } }, ['loadPublishing', 'savePublishing'] as const)
 await c.loadPublishing()
 assert.equal(c.mergeMaxParallel, 2)
 c.mergeMaxParallel = 4
 await c.savePublishing()
 assert.equal(stored.mergeMaxParallel, 4)
 fail = true
 c.mergeMaxParallel = 1
 await c.savePublishing()
 assert.equal(c.mergeMaxParallel, 4)
 assert.match(c.publishError, /write failed/)
})
