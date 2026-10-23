import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { stripTypeScriptTypes } from 'node:module'
import { formatBytes } from '../settings/display.ts'

const source = stripTypeScriptTypes(readFileSync(new URL('./attach.ts', import.meta.url), 'utf8')).replace(/^import .*\n/gm, '').replaceAll('export ', '')
test('uploads use current host limits before reading base64, and retain server errors', async () => {
  let limit = 4
  let reads = 0
  const sent = []
  const context = {
    formatBytes,
    api: { settings: async () => [{ key: 'content.maxFileBytes', value: limit }], attach: async (...args) => { sent.push(args); return { id: 'saved' } } },
    FileReader: class { readAsDataURL() { reads++; this.result = 'data:text/plain;base64,MTIzNDU='; this.onload() } },
  }
  vm.createContext(context); vm.runInContext(source, context)
  const item = { size: 5, name: 'f.txt', file: {} }
  await context.upload('room', item, () => {})
  assert.equal(item.status, 'failed'); assert.match(item.error, /maximum 4 bytes/); assert.equal(reads, 0)
  limit = 5
  await context.upload('room', item, () => {})
  assert.equal(item.status, 'ready'); assert.equal(sent.length, 1); assert.equal(reads, 1)
  limit = 3
  await context.upload('room', item, () => {})
  assert.equal(item.status, 'failed'); assert.match(item.error, /maximum 3 bytes/); assert.equal(reads, 1)
  limit = 25 * 1024 * 1024; item.size = limit + 1
  await context.upload('room', item, () => {})
  assert.match(item.error, /maximum 25 MiB/)
  item.size = 5
  limit = 9; context.api.attach = async () => { throw new Error('maximum 2 bytes') }
  await context.upload('room', item, () => {})
  assert.match(item.error, /maximum 2 bytes/)
})
