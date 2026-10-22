import { test } from 'node:test'
import assert from 'node:assert/strict'
import { detailHref, parseDetailHref } from './details.js'

test('completion links round-trip stored job, run, and exact session turn IDs', () => {
  for (const [kind, prefix] of [['shell', 'sh'], ['run', 'wf'], ['session', 'ses']]) {
    const target = { kind, id: `${prefix}_20261017-123456-abcdef12`, turn: kind === 'session' ? 4 : 0 }
    assert.deepEqual(parseDetailHref(detailHref(target)), target)
  }
  const detail = { kind: 'session', id: 'ses_20261017-123456-abcdef12', turn: 2 }
  assert.deepEqual(parseDetailHref(detailHref(detail, 'room_20261017-123456-abcdef12')), { ...detail, roomId: 'room_20261017-123456-abcdef12' })
})

test('detail links reject commands, external URLs, malformed IDs, and invalid turns', () => {
  for (const target of [null, { kind: 'shell', id: 'make test' }, { kind: 'run', id: '../outside' }, { kind: 'unknown', id: 'x' }]) assert.equal(detailHref(target), '')
  for (const href of [
    'https://example.com/#dyna-detail=shell&id=sh_20261017-123456-abcdef12',
    '#dyna-detail=shell&id=make+test', '#dyna-detail=session&id=ses_20261017-123456-abcdef12&turn=-1',
    '#dyna-detail=session&id=ses_20261017-123456-abcdef12&turn=1.5',
    '#dyna-detail=shell&id=sh_20261017-123456-abcdef12&turn=1',
    '#dyna-detail=shell&id=sh_20261017-123456-abcdef12&room=outside',
  ]) assert.equal(parseDetailHref(href), null, href)
})

test('completion detail links work in global as well as named rooms', () => {
  const detail = { kind: 'session', id: 'ses_20261017-123456-abcdef12', turn: 2 }
  assert.deepEqual(parseDetailHref(detailHref(detail, 'global')), { ...detail, roomId: 'global' })
})
