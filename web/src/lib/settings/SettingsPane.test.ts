import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { compile } from 'svelte/compiler'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'
import { formatBytes, settingDisplay } from './display.ts'
import type { SettingItem } from '../api/settings.ts'
import type { Post } from '../api/room.ts'

const source = readFileSync(new URL('./SettingsPane.svelte', import.meta.url), 'utf8')

// literal evaluates the object or array that the component assigns to a top-level constant.
function literal(name: string): unknown {
  const found = new RegExp(`const ${name}\\b[^=\\n]*= ([\\[{][^]*?\\n  [\\]}])\\n`).exec(source)
  return vm.runInContext(`(${need(found?.[1], name)})`, vm.createContext({}))
}

test('saving either content limit refreshes both derived total descriptions', async () => {
  const context: { items: { key: string; help: string }[]; saved: string; [name: string]: unknown } = {
    items: [{ key: 'content.maxCardBytes', help: 'old total' }, { key: 'content.maxFileBytes', help: 'old total' }],
    errors: {}, drafts: {}, saved: '', setTimeout: () => {}, REVIEWER_KEY: 'reviewer.profile',
    api: {
      setSetting: async (key: string, value: string) => ({ key, value }),
      settings: async () => [{ key: 'content.maxCardBytes', help: 'new total' }, { key: 'content.maxFileBytes', help: 'new total' }],
    },
  }
  runFunctions(source, ['set'], context)
  for (const key of ['content.maxCardBytes', 'content.maxFileBytes']) {
    await call(context, 'set', key, 55)
    assert.equal(need(context.items[0]).help, 'new total'); assert.equal(need(context.items[1]).help, 'new total')
    assert.equal(context.saved, key)
  }
})

test('size and duration controls use human values without changing raw values', () => {
  const sizes: [number, string][] = [[26214400, '25 MiB'], [524288, '512 KiB'], [1610612736, '1.5 GiB'], [1152, '1,152 bytes'], [12000, '12,000 bytes'], [1536, '1.5 KiB'], [1280, '1.25 KiB'], [26214401, '26,214,401 bytes']]
  for (const [value, expected] of sizes) {
    assert.equal(formatBytes(value), expected)
    const item = { kind: 'bytes', value }
    assert.equal(settingDisplay(item), expected)
    assert.equal(item.value, value)
  }
  assert.equal(settingDisplay({ kind: 'duration', value: '2m0s' }), '2m')
  assert.equal(settingDisplay({ kind: 'duration', value: '1h0m0s' }), '1h')
  assert.equal(settingDisplay({ kind: 'duration', value: '1m30s' }), '1m30s')
  assert.match(source, /value=\{drafts\[key\] \?\? settingDisplay\(it\)\}/)
})

test('size edits send human input and unchanged displays do not save', () => {
  const calls: unknown[][] = []
  const drafts: Record<string, string> = { 'content.maxFileBytes': '26,214,401 bytes' }
  const errors: Record<string, string> = {}
  const context = { errors, drafts, settingDisplay, set: (...args: unknown[]) => calls.push(args) }
  runFunctions(source, ['commit'], context)
  // An unchanged exact byte display must not trigger a save.
  const item = { key: 'content.maxFileBytes', kind: 'bytes', value: 26214401 }
  call(context, 'commit', item)
  assert.equal(calls.length, 0)
  drafts[item.key] = '1.5 GiB'
  call(context, 'commit', item)
  assert.deepEqual(calls, [[item.key, '1.5 GiB']])
  errors[item.key] = 'invalid size'
  drafts[item.key] = '26,214,401 bytes'
  call(context, 'commit', item)
  assert.deepEqual(calls[1], [item.key, '26,214,401 bytes'])
})

test('Browser controls follow the selected profile mode', () => {
  const mode: Pick<SettingItem, 'value'> = { value: 'shared' }
  const context = { byKey: { 'browser.profileMode': mode } }
  runFunctions(source, ['visible'], context)
  for (const value of ['shared', 'separate', 'copy-mine']) {
    mode.value = value
    assert.equal(call(context, 'visible', 'browser.saveBack'), value === 'shared')
    assert.equal(call(context, 'visible', 'browser.ownerProfile'), value === 'copy-mine')
    assert.equal(call(context, 'visible', 'browser.extraFlags'), true)
  }
  assert.match(source, /Changes apply at the next Chromium start/)
})

test('Browser keeps a stored unavailable mode visible', () => {
  const context = {}
  runFunctions(source, ['missingLabel'], context)
  assert.equal(call(context, 'missingLabel', { key: 'browser.profileMode', value: 'copy-mine' }), 'Copy my Chromium profile (unavailable)')
  assert.equal(call(context, 'missingLabel', { key: 'browser.ownerProfile', value: 'Profile 9' }), 'Profile 9 (not found)')
  assert.match(source, /<option value=\{it.value\} disabled>/)
})

test('the Feed section previews the real feed with sample posts under the current choices', async () => {
  assert.match(source, /<Feed room=\{sampleRoom\} posts=\{previewPosts\} intro=\{false\}/)
  assert.match(source, /previewPosts = \$derived\(samplePosts\.filter\(visibleTo\(look\)\)\)/)
  assert.doesNotMatch(source, /scopeOf/, 'no part of Settings scopes the look to one channel')
  const { samplePosts } = await import('../rooms/feedSample.ts')
  const { visibleTo, oneLine, highlighted } = await import('../rooms/look.ts')
  const look = { all: {} }
  assert.ok(samplePosts.every(visibleTo(look)))
  assert.ok(!samplePosts.some((p) => oneLine(look, p)), 'the full-text default keeps worker results open')
  assert.equal(samplePosts.filter((p) => highlighted(look, p, 'dyna-agent')).length, 2, 'root’s post and the post to root are highlighted')
})

test('with Finished turns Hidden, the preview drops worker results and keeps root results and failures', async () => {
  const { samplePosts } = await import('../rooms/feedSample.ts')
  const { visibleTo, withChoice } = await import('../rooms/look.ts')
  const look = withChoice({ all: {} }, 'finished', 'hidden')
  const shown: Partial<Post>[] = samplePosts.filter(visibleTo(look))
  assert.ok(!shown.some((p) => p.completion && p.kind === 'completed' && p.speaker !== 'root'), 'worker results hide')
  assert.ok(shown.some((p) => p.completion && p.speaker === 'root'), 'root result stays')
  assert.ok(shown.some((p) => p.kind === 'failed'), 'failure stays')
})

test('rows with a choice control wrap, so the control fits a narrow phone', () => {
  // The radiogroup lives in Segmented, a child component, so a scoped selector
  // never matches it and Svelte drops the rule. Check the compiled CSS.
  const css = need(compile(source, { filename: 'SettingsPane.svelte', css: 'external' }).css, 'css').code
  const rules = css.split('}').filter((r) => r.includes(":has([role='radiogroup'])"))
  assert.ok(rules.some((r) => /\.row\.svelte-\w+:has\(\[role='radiogroup'\]\)\s*\{[^]*flex-wrap: wrap/.test(r)))
  assert.ok(rules.some((r) => /:has\(\[role='radiogroup'\]\) \.text[^{]*\{[^]*flex-basis: 260px/.test(r)))
})
