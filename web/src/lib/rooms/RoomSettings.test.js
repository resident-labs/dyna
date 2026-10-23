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
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({ contents: 'export const api = {}; export const openExternal = () => {}; export const features = {}', loader: 'js' }))
    } }],
  })
  const file = join(dir, 'RoomSettings.mjs')
  await writeFile(file, result.outputFiles[0].contents)
  const module = await import(pathToFileURL(file).href)
  return { dir, component: module.default, render: module.render, createRawSnippet: module.createRawSnippet }
}
const text = (html) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')
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
    const show = (start, props = {}) => render(component, { props: { room, start, onclose() {}, ...props } }).body
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
    assert.doesNotMatch(hooks, /aria-label="Repo"/)
    assert.match(text(show('hooks', { room: { ...room, repo: '' } })), /This room has no repository\./)
    assert.match(text(show('noise')), /Bot noise rules .*Learned bot noise rules/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})

// Run the page's real handlers with a fake api.
const source = readFileSync(new URL('./RoomSettings.svelte', import.meta.url), 'utf8')
const handlers = (names) => parse(source).instance.content.body.filter((node) =>
  (node.type === 'FunctionDeclaration' && names.includes(node.id.name)) ||
  (node.type === 'VariableDeclaration' && names.includes(node.declarations[0]?.id.name))
).map((node) => source.slice(node.start, node.end)).join('\n')
function page(api, names, extra = {}) {
  const context = { api, $state: (value) => value, roomId: 'r', normalizePolicy, budgetForm, checksText, saveRoomPolicy, policy: null, form: null, busy: false, dirty: true, error: '', saved: false, onreviewer: async () => {}, prSettings: null, reviewPublishing: 'local', publishError: '', ...extra }
  vm.createContext(context)
  vm.runInContext(stripTypes(handlers([...names, 'formOf', 'policyRequest', 'policyRoom', 'policyLoading', 'saveAfterPolicy', 'reviewerBusy'])), context)
  return context
}

test('the policy saves all budget fields, reloads them, and reports rejected writes', async () => {
  let stored = { ...DEFAULT_POLICY, ciTestAllowNotes: { 'pkg.Test/a,b': 'External service' } }
  let fail = false
  const api = { roomChangePolicy: async () => stored, setRoomChangePolicy: async (_, next) => {
    if (fail) throw new Error('CI budget requires 0 < warn < fail')
    stored = JSON.parse(JSON.stringify(next))
  } }
  const open = () => page(api, ['load', 'save', 'fill'])
  const first = open()
  await first.load()
  assert.equal(first.form.ciNoJUnitWarning, true)
  Object.assign(first.form, { ciWarnAfter: '4m', ciFailAfter: '5m', ciTestMax: '20s', ciTestAllow: 'pkg.Test/a,b\nSuite.test[x]', ciTestAllowNotes: { 'pkg.Test/a,b': '12 seconds: external service', 'Suite.test[x]': '15 seconds: setup' }, ciNoJUnitWarning: false, requiredChecks: 'one, two' })
  await first.save()
  assert.equal(first.saved, true)
  assert.equal(first.busy, false)
  const reopened = open()
  await reopened.load()
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
  let stored = { reviewerSession: 'reviewer' }
  const api = { prSettings: async () => stored, setPRSettings: async (_, next) => { stored = JSON.parse(JSON.stringify(next)) } }
  const first = page(api, ['loadPublishing', 'savePublishing'])
  await first.loadPublishing()
  assert.equal(first.reviewPublishing, 'local')
  first.reviewPublishing = 'github'
  await first.savePublishing()
  assert.deepEqual(stored, { reviewerProfile: '', reviewerSession: 'reviewer', reviewPublishing: 'github', githubAccess: 'read-only', mergeMaxParallel: 3 })
  const reopened = page(api, ['loadPublishing', 'savePublishing'])
  await reopened.loadPublishing()
  assert.equal(reopened.reviewPublishing, 'github')
  assert.equal(reopened.githubAccess, 'read-only')
  reopened.githubAccess = 'read-write'
  await reopened.savePublishing()
  const accessReopened = page(api, ['loadPublishing', 'savePublishing'])
  await accessReopened.loadPublishing()
  assert.equal(accessReopened.githubAccess, 'read-write')
})

test('a failed publishing write shows the error and returns the select to the stored value', async () => {
  const c = page({ prSettings: async () => ({}), setPRSettings: async () => { throw new Error('write failed') } }, ['loadPublishing', 'savePublishing'])
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
  const opener = /openRoomSettings: (async \(target\) => \{.*\}),\n/.exec(app)[1]
  const mount = /\{#if (showRoomSettings && room)\}\n(?:.*\n)*?\s*<RoomSettings [^\n]*onclose=\{(\(\) => \(showRoomSettings = false\))\} \/>/.exec(app)
  const selected = []
  const state = vm.createContext({ roomId: 'room-1', room, showRoomSettings: false, selectRoom: async (id) => { selected.push(id); state.roomId = id; return true } })
  const { actions, roomMenu } = await import('../ui/actions.ts')
  const item = (r) => roomMenu(actions({ room: r, rooms: [r], icons: {}, openRoomSettings: vm.runInContext(stripTypes(opener), state) })).find((a) => a.id === 'roomsettings')

  await item(room).run()
  assert.ok(vm.runInContext(stripTypes(mount[1]), state))
  assert.deepEqual(selected, [])

  const shell = readFileSync(new URL('../ui/SettingsShell.svelte', import.meta.url), 'utf8')
  assert.match(shell, /role="dialog" aria-modal="true"[^>]*use:modal=\{\{ close: onclose, busy \}\}/)
  const { dialog } = await import('../ui/dialog.ts')
  const listeners = new Set()
  globalThis.window = { addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn) }
  globalThis.document = { activeElement: null }
  const node = { parentElement: { addEventListener() {}, removeEventListener() {} }, focus() {}, contains: () => false, querySelectorAll: () => [], querySelector: () => null }
  const action = dialog(node, { close: vm.runInContext(stripTypes(mount[2]), state), busy: false })
  for (const fn of listeners) fn({ key: 'Escape', preventDefault() {}, stopImmediatePropagation() {} })
  assert.equal(state.showRoomSettings, false)
  action.destroy()
  delete globalThis.window
  delete globalThis.document

  await item({ ...room, id: 'room-2' }).run()
  assert.deepEqual(selected, ['room-2'])
  assert.equal(state.showRoomSettings, true)
})

// The Rules section's real textarea handler and editing effect, inside the
// real dialog action with App's busy={rulesEditing} wiring.
test('Esc in a rule edit cancels the edit, and leaving Rules mid-edit frees Esc for the modal', async () => {
  const rulesSource = readFileSync(new URL('./Rules.svelte', import.meta.url), 'utf8')
  const effect = /\$effect\((\(\) => \{ editing = !!edit;.*?\})\)\n/.exec(rulesSource)[1]
  const rules = vm.createContext({ edit: null, editing: false, save() {}, tick: async () => {}, document: { getElementById: () => null } })
  const handlers = topLevelFunctions(rulesSource, ['editKey', 'cancelEdit', 'restoreEditFocus'])
  vm.runInContext([...handlers.values()].join('\n'), rules)
  const textareaKey = rules.editKey
  let cleanup = () => {}
  const rerun = () => { cleanup(); cleanup = vm.runInContext(stripTypes(effect), rules)() }

  const { dialog } = await import('../ui/dialog.ts')
  const listeners = new Set()
  globalThis.window = { addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn) }
  globalThis.document = { activeElement: null }
  const textarea = {}
  const node = { parentElement: { addEventListener() {}, removeEventListener() {} }, focus() {}, contains: (el) => el === textarea || el === node, querySelectorAll: () => [], querySelector: () => null }
  let closed = 0
  const action = dialog(node, { close: () => closed++, busy: false })
  // window capture first, then the target, as in the browser.
  function escape(target) {
    const event = { key: 'Escape', target, stopped: false, preventDefault() {}, stopImmediatePropagation() { this.stopped = true }, stopPropagation() { this.stopped = true } }
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
  delete globalThis.window
  delete globalThis.document
})

// HooksPane's real onmodal effect with Room settings' real onmodal handler.
test('leaving Hooks with a hook editor open frees Esc for the modal', async () => {
  const hooksSource = readFileSync(new URL('../settings/HooksPane.svelte', import.meta.url), 'utf8')
  const effect = /\$effect\((\(\) => \{ onmodal\?\.\(!!editor\);.*?\})\)\n/.exec(hooksSource)[1]
  const handler = /<HooksPane [^>]*onmodal=\{(\(open\) => \(hooksBusy = open\))\}/.exec(source)[1]
  const state = vm.createContext({ hooksBusy: false })
  const pane = vm.createContext({ editor: { form: {} }, onmodal: vm.runInContext(stripTypes(handler), state) })
  const cleanup = vm.runInContext(stripTypes(effect), pane)()
  assert.equal(state.hooksBusy, true) // Add hook: the editor is open

  const { dialog } = await import('../ui/dialog.ts')
  const listeners = new Set()
  globalThis.window = { addEventListener: (_, fn) => listeners.add(fn), removeEventListener: (_, fn) => listeners.delete(fn) }
  globalThis.document = { activeElement: null }
  const node = { parentElement: { addEventListener() {}, removeEventListener() {} }, focus() {}, contains: (el) => el === node, querySelectorAll: () => [], querySelector: () => null }
  let closed = 0
  const action = dialog(node, { close: () => closed++, busy: state.hooksBusy })
  cleanup() // select General: HooksPane unmounts
  action.update({ close: () => closed++, busy: state.hooksBusy })
  for (const fn of listeners) fn({ key: 'Escape', target: node, preventDefault() {}, stopImmediatePropagation() {} })
  assert.equal(closed, 1)
  action.destroy()
  delete globalThis.window
  delete globalThis.document
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
 let stored={mergeMaxParallel:2}
 let fail=false
 const c=page({prSettings:async()=>stored,setPRSettings:async(_,next)=>{if(fail)throw new Error('write failed');stored=JSON.parse(JSON.stringify(next))}},['loadPublishing','savePublishing'])
 await c.loadPublishing()
 assert.equal(c.mergeMaxParallel,2)
 c.mergeMaxParallel=4
 await c.savePublishing()
 assert.equal(stored.mergeMaxParallel,4)
 fail=true
 c.mergeMaxParallel=1
 await c.savePublishing()
 assert.equal(c.mergeMaxParallel,4)
 assert.match(c.publishError,/write failed/)
})
