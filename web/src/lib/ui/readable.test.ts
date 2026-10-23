import test from 'node:test'
import assert from 'node:assert/strict'
import { fieldLabel, kindOf, recordTitle, recordLine, linkOf, paragraphs } from './readable.ts'

test('field labels read as words', () => {
  assert.equal(fieldLabel('agents_supported'), 'Agents supported')
  assert.equal(fieldLabel('releaseDate'), 'Release date')
  assert.equal(fieldLabel('url'), 'URL')
})

test('values get a kind the view can render', () => {
  assert.equal(kindOf('x'), 'text')
  assert.equal(kindOf(3), 'scalar')
  assert.equal(kindOf(true), 'scalar')
  assert.equal(kindOf(null), 'empty')
  assert.equal(kindOf({ title: 'Docs', url: 'https://tern.build/docs' }), 'link')
  assert.equal(kindOf('https://tern.build'), 'link')
  assert.equal(kindOf([{ a: 1 }, { a: 2 }]), 'records')
  assert.equal(kindOf(['a', 'b']), 'list')
  assert.equal(kindOf({ a: 1 }), 'object')
})

test('a record has a title and a one-line summary from its own fields', () => {
  const r = { tool: 'Tern', summary: 'A Mac app.', confidence: 'high' }
  assert.equal(recordTitle(r), 'Tern')
  assert.equal(recordLine(r), 'A Mac app.')
  assert.equal(recordTitle({ x: 1 }, 3), 'Item 4')
  assert.equal(recordLine({ tool: 'Tern', note: 'First string after the title' }), 'First string after the title')
})

test('links show their title and host', () => {
  assert.deepEqual(linkOf({ title: 'Docs', url: 'https://tern.build/docs' }), { text: 'Docs', url: 'https://tern.build/docs', host: 'tern.build' })
  assert.deepEqual(linkOf('https://x.dev/a'), { text: 'https://x.dev/a', url: 'https://x.dev/a', host: 'x.dev' })
  assert.equal(linkOf({ title: 'bad', url: 'javascript:alert(1)' }), null)
})

test('long text splits into paragraphs', () => {
  assert.deepEqual(paragraphs('one\n\ntwo\nlines\n\n\nthree'), ['one', 'two\nlines', 'three'])
})

test('an object with a url and other fields stays an object; only link-only shapes become links', () => {
  const record = { name: 'Tool', url: 'https://example.com', summary: 'Findings', confidence: 'low' }
  assert.equal(kindOf(record), 'object')
  assert.equal(linkOf(record), null)
  assert.equal(kindOf(record.url), 'link')
  assert.equal(kindOf({ title: 'Docs', url: 'https://tern.build/docs' }), 'link')
  assert.equal(kindOf([record, record]), 'records')
})
