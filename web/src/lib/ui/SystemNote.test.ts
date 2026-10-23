import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { isRecord } from '../api/guards.ts'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { renderedBody } from '../../../test/svelte-render.ts'
import { call } from '../../../test/svelte-source.ts'
import { noteData } from './notices.ts'

const text = (html: string): string => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim()
const slow = (id: string, name: string, time: string, prs: string) => noteData({ id, kind: 'issue', ts: '2026-10-20T03:10:00Z', text: `Slow test over the CI limit: dyna-agent/internal/x/${name}\n\nTime: ${time}, limit 1 s\nPRs: ${prs}\nArea: internal/x\n\nnote` })

// SystemNote rendered on the server, built once for the file.
const dir = await mkdtemp(join(tmpdir(), 'dyna-system-note-'))
const result = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
  stdin: { contents: "export { default } from './SystemNote.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
  plugins: [svelteEsbuildPlugin('server')],
})
const bundled = result.outputFiles?.[0]
assert.ok(bundled !== undefined, 'SystemNote did not compile')
await writeFile(join(dir, 'SystemNote.mjs'), bundled.contents)
const loaded: unknown = await import(pathToFileURL(join(dir, 'SystemNote.mjs')).href)
if (!isRecord(loaded) || !('default' in loaded)) assert.fail('SystemNote bundle has no component')
const SystemNote = loaded.default
test.after(() => rm(dir, { recursive: true, force: true }))

test('one slow test reads as one line: name, time against the limit, and PRs', () => {
  const html = renderedBody(call(loaded, 'render', SystemNote, { props: { notes: [slow('m1', 'TestA', '2.47 s', '#1104, #1138')], roomId: 'r', onissue: () => {} } }))
  assert.match(text(html), /^✦ Slow test TestA · 2\.47 s of 1 s · #1104, #1138/)
  assert.match(html, /href="#room=r&amp;pr=1104"/)
  assert.match(text(html), /TestA 2\.47 s \/ 1 s internal\/x · #1104 ?, #1138 Issue ›/)
})

test('several slow tests fold into one row that opens to one line each', () => {
  const html = renderedBody(call(loaded, 'render', SystemNote, { props: { notes: [slow('m1', 'TestA', '2.47 s', '#1104'), slow('m2', 'TestB', '1.2 s', '#1138')], roomId: 'r' } }))
  assert.match(text(html), /^✦ 2 slow tests over the 1 s limit/)
  assert.match(text(html), /TestA 2\.47 s \/ 1 s internal\/x · #1104 TestB 1\.2 s \/ 1 s internal\/x · #1138/)
  // Without a way to open issues, no Issue button shows.
  assert.doesNotMatch(html, /Issue ›/)
})
