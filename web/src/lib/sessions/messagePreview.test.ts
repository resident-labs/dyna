import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { domNames, installDom } from '../prs/prDetailHarness.ts'

const dom = installDom()
for (const name of ['DOMParser', 'NodeFilter']) Reflect.set(globalThis, name, Reflect.get(dom.window, name))
after(() => {
  dom.window.close()
  for (const name of [...domNames, 'DOMParser', 'NodeFilter']) Reflect.deleteProperty(globalThis, name)
})

const compiled = await build({
  bundle: true, write: false, platform: 'node', format: 'esm', conditions: ['node'], logLevel: 'silent',
  stdin: {
    contents: `export { default as DmRows } from './sessions/DmRows.svelte';
      export { default as Feed } from './rooms/Feed.svelte';
      export { dmView } from './sessions/dm.ts';
      export { render } from 'svelte/server'`,
    resolveDir: new URL('..', import.meta.url).pathname,
  },
  plugins: [svelteEsbuildPlugin('server')],
})
const output = compiled.outputFiles?.[0]
assert.ok(output)
const { DmRows, Feed, dmView, render } = await import('data:text/javascript;base64,' + Buffer.from(output.contents).toString('base64'))

const tail = 'This final sentence must remain visible in the session DM.'
const text = `The first sentence gives the result. ${'The middle adds enough detail to need a preview. '.repeat(8)}${tail}`
const post = { id: 'msg_long', from: 'room:agent', to: ['room'], ts: 1, text }
const room = { id: 'room', name: 'room' }
const member = { id: 'agent', name: 'agent', address: 'room:agent', state: 'idle' }

test('a session DM shows a long message in full while Room chat keeps its preview', () => {
  const feed = render(Feed, { props: { room, posts: [post] } }).body
  assert.match(feed, /Show more/)
  assert.doesNotMatch(feed, new RegExp(tail))

  const dm = dmView({ address: member.address, posts: [post], turns: [] }, room)
  const transcript = render(DmRows, { props: {
    dm, room, member, members: [member], onthread() {}, onopenfile() {},
  } }).body
  assert.match(transcript, new RegExp(tail))
  assert.doesNotMatch(transcript, /Show (?:more|less)/)
})
