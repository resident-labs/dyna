import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'

// MediaAttachment is the part of an attachment the media grid reads.
type MediaAttachment = { id: string; name: string; mime: string }

const compiled = await build({
  bundle: true, write: false, platform: 'node', format: 'esm', logLevel: 'silent',
  stdin: { contents: "export { default as Media } from './SessionMedia.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname },
  plugins: [svelteEsbuildPlugin('server')],
})
const output = compiled.outputFiles?.[0]
assert.ok(output)
const { Media, render } = await import('data:text/javascript;base64,' + Buffer.from(output.contents).toString('base64'))
const post = (id: string, day: number, attachments?: MediaAttachment[]) => ({ id, ts: Date.UTC(2026, 9, day, 12), from: 'room:agent', to: ['room'], text: 'Text', attachments })
const room = { id: 'room', name: 'room' }
const html = (posts: ReturnType<typeof post>[]): string => render(Media, { props: { room, posts, onopenfile() {} } }).body

test('media groups only attachment days and text-only input shows the empty state', () => {
  const empty = html([post('p1', 1), post('p2', 2, [])])
  assert.match(empty, /No media/)
  assert.doesNotMatch(empty, /<h3/)
  const one = { id: 'a', name: 'first.png', mime: 'image/png' }
  const two = { id: 'b', name: 'second.png', mime: 'image/png' }
  const three = { id: 'c', name: 'third.png', mime: 'image/png' }
  const populated = html([post('text', 1), post('p3', 2, [one, two]), post('text2', 3, []), post('p4', 4, [three])])
  assert.equal((populated.match(/<h3/g) ?? []).length, 2)
  assert.ok(populated.indexOf('first.png') < populated.indexOf('second.png'))
  assert.ok(populated.indexOf('second.png') < populated.indexOf('third.png'))
  assert.doesNotMatch(populated, /No media/)
})
