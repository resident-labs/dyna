import test from 'node:test'
import assert from 'node:assert/strict'
import { previewLine } from './runPreview.ts'

// The row preview of a run agent shows a sentence, never the braces of a result
// (msg_ccbcdf34bb0b6697).
test('a JSON result reads as the same one-line summary the full view gives', () => {
  assert.equal(previewLine('{"tool":"T3 Code","product_found":"Yes — it works"}'), 'Yes — it works')
  assert.equal(previewLine('{"summary":"Three tools matched"}'), 'Three tools matched')
  assert.equal(previewLine('[{"name":"tierlist","note":"Best pick"},{"name":"other","note":"Second"}]'), 'Best pick')
  assert.equal(previewLine('42'), '42')
})

test('plain text and a cut-off JSON preview keep the row honest', () => {
  assert.equal(previewLine('Found three tools on the page.'), 'Found three tools on the page.')
  assert.equal(previewLine(''), '')
  assert.equal(previewLine('{"tool":"T3 Code","product_'), '', 'a preview cut inside JSON shows nothing')
  assert.equal(previewLine('["first","sec'), '')
  assert.equal(previewLine('{"tool":"T3 Code"}'), '', 'a record with no sentence has no preview')
})
