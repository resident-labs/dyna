import { test } from 'node:test'
import assert from 'node:assert/strict'
import { get } from 'svelte/store'
import { attachmentDraft, type AttachmentDraft, type DraftItem } from './attachmentDraft.ts'

const file = (name = 'test.png') => new File(['1234'], name, { type: 'image/png' })
const settled = () => new Promise((resolve) => setImmediate(resolve))
const first = (draft: AttachmentDraft): DraftItem => {
  const item = get(draft).items[0]
  assert.ok(item)
  return item
}

class FakeFileReader {
  result: string | null = null
  onload: (() => void) | null = null
  readAsDataURL() { this.result = 'data:image/png;base64,MTIzNA=='; this.onload?.() }
}

function setup() {
  const revoked: string[] = [], sent: unknown[] = [], errors: string[] = []
  let limit = 8, room: string | undefined = 'room_a', closed = false, focused = 0
  URL.createObjectURL = (blob) => `blob:${blob instanceof File ? blob.name : ''}`
  URL.revokeObjectURL = (url) => { revoked.push(url) }
  Object.defineProperty(globalThis, 'FileReader', { value: FakeFileReader, configurable: true, writable: true })
  globalThis.fetch = async (input, init) => {
    const path = String(input)
    assert.equal(init?.credentials, 'same-origin')
    if (path === '/api/Settings') return Response.json([{ key: 'content.maxFileBytes', value: limit }])
    assert.equal(path, '/api/Attach')
    const body = init?.body
    if (typeof body !== 'string') throw new Error('attach needs a JSON body')
    sent.push(JSON.parse(body))
    return Response.json({ id: `att_${sent.length}` })
  }
  const draft = attachmentDraft({ room: () => room, error: (message) => errors.push(message), focus: () => focused++, disabled: () => closed })
  return { draft, revoked, sent, errors, focused: () => focused, limit: (value: number) => { limit = value }, room: (value: string | undefined) => { room = value }, close: () => { closed = true } }
}

test('staging caps files and releases every preview on clear', async () => {
  const s = setup()
  s.draft.addFiles(Array.from({ length: 10 }, (_, i) => file(`${i}.png`)))
  assert.equal(get(s.draft).items.length, 8)
  assert.equal(get(s.draft).uploading, true)
  assert.equal(s.errors[0], 'A message can carry at most 8 files.')
  await settled()
  assert.equal(get(s.draft).ready.length, 8)
  assert.equal(get(s.draft).atts[0]?.name, '0.png')
  assert.equal(s.sent.length, 8)
  assert.equal(s.focused(), 1)
  s.draft.clear()
  assert.equal(get(s.draft).items.length, 0)
  assert.equal(s.revoked.length, 8)
  s.draft.destroy()
})

test('failed uploads remain until retry or removal', async () => {
  const s = setup()
  s.limit(1)
  s.draft.addFiles([file()])
  await settled()
  assert.equal(get(s.draft).failed, true)
  assert.equal(get(s.draft).atts.length, 0)
  s.limit(8)
  s.draft.retry(first(s.draft))
  await settled()
  assert.equal(get(s.draft).failed, false)
  assert.equal(get(s.draft).ready.length, 1)
  s.draft.remove(first(s.draft))
  assert.deepEqual(s.revoked, ['blob:test.png'])
  s.draft.destroy()
})

test('removed or destroyed uploads cannot restore stale items', async () => {
  const s = setup()
  s.draft.addFiles([file()])
  s.draft.remove(first(s.draft))
  await settled()
  assert.equal(get(s.draft).items.length, 0)
  s.draft.addFiles([file('late.png')])
  s.draft.destroy()
  await settled()
  assert.equal(get(s.draft).items.length, 0)
  s.draft.addFiles([file('ignored.png')])
  assert.equal(get(s.draft).items.length, 0)
  assert.deepEqual(s.revoked, ['blob:test.png', 'blob:late.png'])
})

test('text paste stays native and closed sessions do not stage files', async () => {
  const s = setup()
  let prevented = false
  await s.draft.paste({ clipboardData: { getData: () => 'text', files: [file()] }, preventDefault() { prevented = true } })
  assert.equal(prevented, false)
  assert.equal(get(s.draft).items.length, 0)
  s.close()
  s.draft.addFiles([file()])
  assert.equal(get(s.draft).items.length, 0)
  s.draft.destroy()
})

test('pending paste cannot attach to a different room', async () => {
  const s = setup()
  const pasting = s.draft.paste({ clipboardData: { getData: () => '', files: [file()] }, preventDefault() {} })
  s.room('room_b')
  await pasting
  assert.equal(get(s.draft).items.length, 0)
  s.draft.destroy()
})
