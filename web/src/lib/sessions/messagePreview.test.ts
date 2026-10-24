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
      export { choose } from './rooms/look.svelte.ts';
      export { render } from 'svelte/server'`,
    resolveDir: new URL('..', import.meta.url).pathname,
  },
  plugins: [svelteEsbuildPlugin('server')],
})
const output = compiled.outputFiles?.[0]
assert.ok(output)
const { DmRows, Feed, dmView, choose, render } = await import('data:text/javascript;base64,' + Buffer.from(output.contents).toString('base64'))

const tail = 'This final sentence must remain visible in the session DM.'
const text = `The first sentence gives the result. ${'The middle adds enough detail to need a preview. '.repeat(8)}${tail}`
const post = { id: 'msg_long', from: 'room:agent', to: ['room'], ts: 1, text }
const room = { id: 'room', name: 'room' }
const member = { id: 'agent', name: 'agent', address: 'room:agent', state: 'idle' }

test('Room chat trims an agent message only on request, and a DM always shows it in full', () => {
  try {
    const open = render(Feed, { props: { room, posts: [post] } }).body
    assert.doesNotMatch(open, /Show (?:more|less)/)
    assert.match(open, new RegExp(tail))

    choose('trim', true)
    const trimmed = render(Feed, { props: { room, posts: [post] } }).body
    assert.match(trimmed, /Show more/)
    assert.doesNotMatch(trimmed, new RegExp(tail))

    const dm = dmView({ address: member.address, posts: [post], turns: [] }, room)
    const transcript = render(DmRows, { props: {
      dm, room, member, members: [member], onthread() {}, onopenfile() {},
    } }).body
    assert.match(transcript, new RegExp(tail))
    assert.doesNotMatch(transcript, /Show (?:more|less)/)
  } finally {
    choose('trim', null)
  }
})

test('a turn result drops its lead-in and follows the Room chat trim setting', () => {
  const turnRoom = { ...room, name: 'dyna' }
  const turnTail = 'This final sentence of the turn report must remain visible in the session DM.'
  const report = `The first paragraph gives the result.\n\n${'More detail makes the report long enough to fold. '.repeat(8)}${turnTail}`
  const payload = { session: 'dyna:root', sessionId: 'ses_root', turn: 490, state: 'completed' as const, summary: report }
  const turnPost = {
    id: 'msg_turn', kind: 'turn', notice: { kind: 'completed' }, payload,
    from: 'dyna', speaker: 'root', to: ['dyna'], ts: 1,
    text: `dyna:root turn 490 completed. Result:\n\n${report}`, fullResult: true,
  }
  try {
    const open = render(Feed, { props: { room: turnRoom, posts: [turnPost] } }).body
    assert.doesNotMatch(open, /dyna:root turn 490 completed\.\s*Result:/)
    assert.doesNotMatch(open, /Show (?:more|less)/)
    assert.match(open, new RegExp(turnTail))

    choose('trim', true)
    const trimmed = render(Feed, { props: { room: turnRoom, posts: [turnPost] } }).body
    assert.doesNotMatch(trimmed, /dyna:root turn 490 completed\.\s*Result:/)
    assert.match(trimmed, /Show more/)
    assert.doesNotMatch(trimmed, new RegExp(turnTail))

    const dm = dmView({ address: 'dyna:root', posts: [{ ...turnPost, fullResult: false }], turns: [] }, turnRoom)
    const root = { id: 'root', name: 'root', address: 'dyna:root', state: 'idle' }
    const transcript = render(DmRows, { props: {
      dm, room: turnRoom, member: root, members: [root], onthread() {}, onopenfile() {},
    } }).body
    assert.match(transcript, new RegExp(turnTail))
    assert.doesNotMatch(transcript, /Show (?:more|less)/)
  } finally {
    choose('trim', null)
  }
})

test('root posts never trim in Room chat', () => {
  const rootTail = 'This final root sentence always stays visible.'
  const agentTail = 'This final agent sentence hides when trimming is on.'
  const report = (last: string) => `The first paragraph gives the result.\n\n${'More detail makes the report long enough to fold. '.repeat(8)}${last}`
  const rootPost = { id: 'msg_root', from: 'room', speaker: 'root', to: ['room:agent'], ts: 1, text: report(rootTail) }
  const agentPost = { ...post, id: 'msg_agent', text: report(agentTail) }
  try {
    choose('trim', true)
    const body = render(Feed, { props: { room, posts: [rootPost, agentPost] } }).body
    const fragment = document.createElement('div')
    fragment.innerHTML = body
    const rootRow = fragment.querySelector('article[data-id="msg_root"]')
    assert.ok(rootRow)
    assert.match(rootRow.textContent ?? '', new RegExp(rootTail))
    assert.doesNotMatch(rootRow.textContent ?? '', /Show (?:more|less)/)
    const agentRow = fragment.querySelector('article[data-id="msg_agent"]')
    assert.ok(agentRow)
    assert.match(agentRow.textContent ?? '', /Show more/)
    assert.doesNotMatch(agentRow.textContent ?? '', new RegExp(agentTail))
  } finally {
    choose('trim', null)
  }
})
