import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// The bar's title lives in lib/app/shell/PageHeader.svelte.
const header: string = readFileSync(new URL('../app/shell/PageHeader.svelte', import.meta.url), 'utf8')

test('a long work-view title shrinks and ends in an ellipsis, so the Call Voice button never cuts it mid-word', () => {
  assert.match(header, /<span class="title" title=\{room\.repo\}><span class="hash">#<\/span><span class="name">\{workRow\.label\}<\/span><\/span>/)
  assert.match(header, /\.title \{[^}]*min-width: 0/)
  assert.match(header, /\.title \.name \{[^}]*overflow: hidden[^}]*text-overflow: ellipsis/)
})
