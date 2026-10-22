import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { blankProfile, toggleUse } from './profiles/state.ts'

// ProfileUses rendered on the server, built once for the file.
const dir = await mkdtemp(join(tmpdir(), 'dyna-profile-uses-'))
const result = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
  stdin: { contents: "export { default } from './ProfileUses.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
  plugins: [svelteEsbuildPlugin('server')],
})
await writeFile(join(dir, 'ProfileUses.mjs'), result.outputFiles?.[0]?.contents ?? '')
const { default: ProfileUses, render } = await import(pathToFileURL(join(dir, 'ProfileUses.mjs')).href)
after(() => rm(dir, { recursive: true, force: true }))

const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
const all = ['workflows', 'agents', 'services']

test('the uses grid has a row per profile and a column per use', () => {
  const profiles = [{ name: 'opus', uses: all }, { name: 'sol', uses: ['services'] }, { name: 'off', uses: all, disabled: true }]
  const html: string = render(ProfileUses, { props: { profiles } }).body
  assert.match(text(html), /^Profile Workflows Agents Services opus sol off/)
  const boxes = [...html.matchAll(/<input[^>]*>/g)].map((m) => m[0])
  assert.equal(boxes.length, 9)
  assert.deepEqual(boxes.map((b) => /checked/.test(b)), [true, true, true, false, false, true, true, true, true])
  // The last use stays on, and a disabled profile cannot be changed here.
  assert.deepEqual(boxes.map((b) => /disabled/.test(b)), [false, false, false, false, false, true, true, true, true])
  assert.match(boxes[0] ?? '', /aria-label="opus: workflows"/)
})

test('toggleUse keeps the display order; a new profile starts with every use', () => {
  assert.deepEqual(toggleUse(['services'], 'workflows', true), ['workflows', 'services'])
  assert.deepEqual(toggleUse(all, 'agents', false), ['workflows', 'services'])
  assert.deepEqual(blankProfile().uses, all)
})

test('the uses grid is the one place to set uses', () => {
  const editor = readFileSync(new URL('./ProfileEditor.svelte', import.meta.url), 'utf8')
  const pane = readFileSync(new URL('./ProfilesPane.svelte', import.meta.url), 'utf8')
  assert.doesNotMatch(editor, /Use for|USES|toggleUse/)
  assert.match(pane, /<ProfileUses /)
  assert.doesNotMatch(pane, /useLabel/)
})
