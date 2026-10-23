import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'

test('PDF page input accepts only whole pages within the document', () => {
  const source = readFileSync(new URL('./PDFPreview.svelte', import.meta.url), 'utf8')
  const context = {}
  runFunctions(source, ['parsePageInput'], context)
  for (const [input, page] of [['1', 1], [' 40 ', 40], ['80', 80]] as const) {
    assert.equal(call(context, 'parsePageInput', input, 80), page)
  }
  for (const input of ['', ' ', '0', '-1', '1.5', '81', '1e1', 'oops', '999999999999999999999']) {
    assert.equal(call(context, 'parsePageInput', input, 80), null, input)
  }
})
