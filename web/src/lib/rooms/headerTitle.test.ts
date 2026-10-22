import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const app: string = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')

test('a long work-view title shrinks and ends in an ellipsis, so the Call Voice button never cuts it mid-word', () => {
  assert.match(app, /<span class="title" title=\{room\.repo\}><span class="hash">#<\/span><span class="name">\{workRow\.label\}<\/span><\/span>/)
  assert.match(app, /\.title \{[^}]*min-width: 0/)
  assert.match(app, /\.title \.name \{[^}]*overflow: hidden[^}]*text-overflow: ellipsis/)
})
