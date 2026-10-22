import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { postHref, postAttachment, speaker } from './posts.js'

const room = 'room_20261017-123456-abcdef12'
test('post links name stored room and post IDs and reject non-post targets', () => {
  const hash = postHref(room, 'msg_123')
  assert.equal(new URLSearchParams(hash.slice(1)).get('room'), room)
  assert.equal(new URLSearchParams(hash.slice(1)).get('post'), 'msg_123')
  for (const invalid of ['', '../file', 'make test', 'https://example.com', 'x'.repeat(65)]) assert.equal(postHref(room, invalid), '')
  assert.equal(postHref('not-a-room', 'msg_123'), '')
})
test('the same attachment opens in the selected post context without changing stored data', () => {
  const attachment = { id: 'file', name: 'evidence.txt', view: 'source' }
  assert.deepEqual(postAttachment(attachment, 'msg_first'), { ...attachment, postId: 'msg_first' })
  assert.equal(postAttachment(attachment, 'msg_second').postId, 'msg_second')
  assert.ok(!('postId' in attachment))
  assert.equal(postAttachment(attachment, ''), attachment)
})

const source = readFileSync(new URL('./PostLink.svelte', import.meta.url), 'utf8')
const open = parse(source).instance.content.body.find((node) => node.type === 'FunctionDeclaration' && node.id.name === 'open')
test('source links support in-app navigation and native modified clicks', () => {
  const calls = []
  const c = { onopen: (id) => calls.push(id), postId: 'msg_123' }
  vm.createContext(c)
  vm.runInContext(source.slice(open.start, open.end), c)
  const event = { button: 0, preventDefault() { calls.push('prevent') } }
  c.open(event)
  assert.deepEqual(calls, ['prevent', 'msg_123'])
  for (const modifier of ['ctrlKey', 'metaKey', 'shiftKey', 'altKey']) c.open({ ...event, [modifier]: true })
  c.open({ ...event, button: 1 })
  c.onopen = null
  c.open(event)
  assert.deepEqual(calls, ['prevent', 'msg_123'])
})

test('Files sends both ordinary previews and card source views through the selected post context', () => {
  const files = readFileSync(new URL('../files/Files.svelte', import.meta.url), 'utf8')
  const declaration = parse(files).instance.content.body.find((node) => node.type === 'VariableDeclaration' && node.declarations.some((d) => d.id.name === 'open'))
  const opened = []
  const c = { postAttachment, postId: 'msg_stored', onopen: (attachment) => opened.push(attachment) }
  vm.createContext(c)
  vm.runInContext(files.slice(declaration.start, declaration.end), c)
  for (const attachment of [{ id: 'file' }, { id: 'image', mime: 'image/png' }, { id: 'card', view: 'source' }]) {
    c.attachment = attachment
    vm.runInContext('open(attachment)', c)
    assert.deepEqual(opened.at(-1), { ...attachment, postId: 'msg_stored' })
    assert.ok(!('postId' in attachment))
  }
})

test('global is a stored room and has the same post links', () => {
  assert.equal(postHref('global', 'msg_123'), '#room=global&post=msg_123')
})
test('speaker tells the human owner from root and keeps server labels', () => {
  assert.equal(speaker({ from: 'alpha', human: true }, 'alpha'), 'you')
  assert.equal(speaker({ from: 'alpha' }, 'alpha'), 'root')
  assert.equal(speaker({ from: 'alpha:lead' }, 'alpha'), 'lead')
  assert.equal(speaker({ from: 'alpha', speaker: 'workflow review' }, 'alpha'), 'workflow review')
})
