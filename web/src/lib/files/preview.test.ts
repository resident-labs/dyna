import { test } from 'node:test'
import assert from 'node:assert/strict'
import { parseTable, formatJSON, previewKind, TABLE_ROWS, type PreviewKind } from './preview.ts'

test('table reader preserves quoted delimiters, quotes, line breaks, BOM, and empty cells', () => {
  const text = '﻿Name,Note,Value\r\n"A, B","Said ""yes""\nand left",0\r\nC,,\r\n'
  assert.deepEqual(parseTable(text).rows, [
    ['Name', 'Note', 'Value'], ['A, B', 'Said "yes"\nand left', '0'], ['C', '', ''],
  ])
  assert.deepEqual(parseTable('A\tB\n1\t"two\tthree"', '\t').rows, [['A', 'B'], ['1', 'two\tthree']])
})

test('table reader refuses broken quoting instead of silently changing cells', () => {
  for (const text of ['a,"unfinished', 'a,"done"x', 'a,quo"te']) assert.throws(() => parseTable(text), /quote/)
})

test('table preview bounds rows and columns and reports truncation', () => {
  const exact = Array.from({ length: TABLE_ROWS + 1 }, (_, i) => `${i},value`).join('\n') + '\n'
  assert.equal(parseTable(exact).more, false)
  const result = parseTable(exact + 'extra,row\n')
  assert.equal(result.rows.length, TABLE_ROWS + 1)
  assert.equal(result.more, true)
  const wide = parseTable(Array.from({ length: 105 }, (_, i) => i).join(','))
  assert.equal(wide.rows[0]?.length, 100)
  assert.equal(wide.columnsTruncated, true)
})

test('preview types cover stored MIME values and file names without executing HTML or SVG', () => {
  const cases: [string, string, PreviewKind][] = [
    ['NOTES.MD', 'text/plain', 'markdown'], ['data.tsv', 'text/plain', 'table'],
    ['data.csv', 'application/octet-stream', 'table'], ['values', 'application/ld+json', 'json'],
    ['report.docx', 'application/zip', 'word'], ['report.pdf', 'application/octet-stream', 'pdf'],
    ['old.doc', 'application/msword', 'file'], ['file.html', 'text/plain', 'text'],
    ['file.svg', 'image/svg+xml', 'file'], ['photo.png', 'image/png', 'image'],
  ]
  for (const [name, mime, expected] of cases) assert.equal(previewKind({ name, mime }), expected, name)
  assert.equal(previewKind({ name: 'card.html', mime: 'text/html', render: 'html' }), 'card')
})

test('JSON formatting preserves large integers, duplicate keys, and string escapes', () => {
  const original = '{"id":9007199254740993,"values":[{},[],"a, b: c"],"id":1e20}'
  const formatted = formatJSON(original)
  assert.ok(formatted.includes('9007199254740993'))
  assert.ok(formatted.includes('1e20'))
  assert.equal((formatted.match(/"id"/g) || []).length, 2)
  assert.deepEqual(JSON.parse(formatted), JSON.parse(original))
  assert.throws(() => formatJSON('{"a":1,}'))
})
