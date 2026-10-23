import test from 'node:test'
import assert from 'node:assert/strict'
import { snippet } from './searchSnippet.ts'

const show = (text: string, query: string, width = 140): string => snippet(text, query, width).map((part) => part.text).join('')

test('an excerpt reads as plain words, without markup or goal IDs', () => {
  const text = '- `retries-dead`: **MergeEvent.Retries** is removed in g317/i4, so [the PR](#288) lands'
  assert.equal(show(text, 'retries'), 'retries-dead: MergeEvent.Retries is removed in, so the PR lands')
  assert.equal(show('Goal item i12 is done', 'item'), 'Goal item is done')
})

test('an excerpt starts at a word and keeps the match on screen', () => {
  const shown = show('aaaaaaaaaaaaaaaaaaaa needle and then the rest of the line pads out', 'needle', 40)
  assert.ok(shown.startsWith('…needle'), shown)
  // A match inside a long word keeps that word, even when the lead falls inside it.
  assert.match(show('one two three four five verylongneedleword and more text to fill the line', 'needle', 40), /verylongneedleword/)
})
