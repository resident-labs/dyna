import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { isRecord } from '../api/guards.ts'
import { errorText } from '../errorText.ts'
import { stripTypes, call } from '../../../test/svelte-source.ts'

interface Request {
  room: string
  key: string
  resolve: (value: unknown) => void
  reject: (error: Error) => void
}
type Context = Record<string, unknown>

function loader() {
  const source = readFileSync(new URL('./ChangeDetail.svelte', import.meta.url), 'utf8')
  const body = stripTypes(source.slice(source.indexOf('  function loadDetail('), source.indexOf('\n  $effect(')))
  const requests: Request[] = []
  const context: Context = vm.createContext({ selection: '', detail: null, error: '', loading: false, errorText, api: { changeDetail: (room: string, key: string) => new Promise((resolve, reject) => requests.push({ room, key, resolve, reject })) } })
  vm.runInContext(body, context)
  return { context, requests }
}
const settle = () => new Promise(setImmediate)
const load = (c: Context, row: object | null, room: string): unknown => call(c, 'loadDetail', row, room)
// the detail the component holds, as a record, or null
function detailOf(c: Context): Record<string, unknown> | null {
  const detail = c.detail
  return isRecord(detail) ? detail : null
}
function request(requests: Request[], index: number): Request {
  const found = requests[index]
  assert.ok(found, `request ${index} exists`)
  return found
}

test('the selected row loads one detail; unselected cards do not load', async () => {
  const { context: c, requests } = loader()
  load(c, null, 'room')
  assert.equal(requests.length, 0)
  load(c, { key: 'change' }, 'room')
  assert.equal(requests.length, 1)
  assert.equal(request(requests, 0).room, 'room')
  assert.equal(request(requests, 0).key, 'change')
  request(requests, 0).resolve({ key: 'change', summary: 'The complete review.' })
  await settle()
  assert.equal(detailOf(c)?.summary, 'The complete review.')
  assert.equal(c.loading, false)
})

test('a previous selection cannot replace detail or error for the current row', async () => {
  const { context: c, requests } = loader()
  const cancel = load(c, { key: 'old' }, 'room-a')
  assert.equal(typeof cancel, 'function')
  if (typeof cancel === 'function') cancel()
  load(c, { key: 'current' }, 'room-b')
  request(requests, 1).resolve({ key: 'current', summary: 'Current' })
  await settle()
  request(requests, 0).reject(new Error('Old selection failed'))
  await settle()
  assert.equal(detailOf(c)?.key, 'current')
  assert.equal(c.error, '')
  assert.equal(c.loading, false)
})

test('a current detail failure stops loading and shows its error', async () => {
  const { context: c, requests } = loader()
  load(c, { key: 'change' }, 'room')
  request(requests, 0).reject(new Error('The review is unavailable.'))
  await settle()
  assert.equal(c.detail, null)
  assert.equal(c.loading, false)
  assert.equal(c.error, 'The review is unavailable.')
})


test('refreshing the same selection keeps its review mounted', async () => {
  const { context: c, requests } = loader()
  load(c, { key: 'change' }, 'room')
  request(requests, 0).resolve({ key: 'change', summary: 'First review' })
  await settle()
  const before = c.detail
  load(c, { key: 'change', checked: 'later' }, 'room')
  assert.equal(c.detail, before)
  request(requests, 1).resolve({ key: 'change', summary: 'Updated review' })
  await settle()
  assert.equal(detailOf(c)?.summary, 'Updated review')
  load(c, { key: 'different' }, 'room')
  assert.equal(c.detail, null)
})
