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

// An owner's own post names the sender "you" once. The row must not repeat
// the identity as a second "human" tag, while other senders keep their labels.
const rooms = fileURLToPath(new URL('.', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-feed-message-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})

const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  loader: { '.svg': 'dataurl' },
  stdin: {
    contents: "export { default as FeedMessage } from './FeedMessage.svelte';\nexport { mount, unmount, flushSync } from 'svelte';",
    resolveDir: rooms,
    loader: 'js',
  },
  plugins: [svelteEsbuildPlugin()],
})
const app = join(dir, 'feed-message.mjs')
await writeFile(app, need(built.outputFiles?.[0]).contents)
const web = await import(pathToFileURL(app).href)

function render(post: Record<string, unknown>) {
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.FeedMessage, { target, props: { post: { id: 'msg_1', ts: 1, text: 'Ship it', fromHue: 1, ...post }, room: { name: 'dyna-agent' } } })
  web.flushSync()
  const header = target.querySelector('header')?.textContent ?? ''
  return { header: header.replace(/\s+/g, ' ').trim(), done: () => { web.unmount(mounted); target.remove() } }
}

test('an owner post shows "you" and no "human" tag', () => {
  const view = render({ from: 'dyna-agent', owner: true, human: true })
  assert.match(view.header, /\byou\b/)
  assert.doesNotMatch(view.header, /human/)
  view.done()
})
