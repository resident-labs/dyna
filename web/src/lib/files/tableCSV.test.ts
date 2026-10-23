import test from 'node:test'
import assert from 'node:assert/strict'
import { tableCSV } from './tableCSV.ts'
import { parseTable } from './preview.ts'

test('visible table CSV preserves exact values and only the supplied columns', () => {
  const rows = [
    ['name', 'note', 'tail'],
    ['Alice', 'comma, and "quote"', ''],
    ['雪', 'line one\r\nline two\tindented', 'end'],
    ['short'],
  ]
  const csv = tableCSV(rows, 3)
  assert.deepEqual(parseTable(csv).rows, [...rows.slice(0, 3), ['short', '', '']])
  assert.equal(tableCSV([['one', 'two', 'hidden']], 2), 'one,two')
  assert.equal(tableCSV([], 3), '')
  const oneColumn = parseTable('name\nAlice\n\n').rows
  assert.deepEqual(parseTable(tableCSV(oneColumn, 1)).rows, oneColumn)
  assert.deepEqual(parseTable(tableCSV([['']], 1)).rows, [['']])
})
