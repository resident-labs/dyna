import test from 'node:test'
import assert from 'node:assert/strict'
import { snippet } from './searchSnippet.ts'

const show = (text: string, query: string, width = 140): string => snippet(text, query, width).map((part) => part.text).join('')

test('an excerpt reads as plain words, without markup or goal IDs', () => {
  const text = '- `retries-dead`: **MergeEvent.Retries** is removed in g317/i4, so [the PR](#288) lands'
  assert.equal(show(text, 'retries'), 'retries-dead: MergeEvent.Retries is removed in, so the PR lands')
  assert.equal(show('Goal item i12 is done', 'item'), 'Goal item is done')
})

test('code keeps its characters: a glob search still finds its text', () => {
  assert.equal(show('Use `src/**/*.ts` for the check', 'src'), 'Use src/**/*.ts for the check')
  // The searched glob is still there to highlight.
  assert.ok(snippet('Use `src/**/*.ts` for the check', 'src/**/*.ts').some((part) => part.hit && part.text === 'src/**/*.ts'))
  // Only a fence line goes, never the line of code below it.
  assert.equal(show('Checked:\n```go\nif src != nil {}\n```\nDone', 'checked'), 'Checked: if src != nil {} Done')
})

test('an excerpt starts at a word and keeps the match on screen', () => {
  const shown = show('aaaaaaaaaaaaaaaaaaaa needle and then the rest of the line pads out', 'needle', 40)
  assert.ok(shown.startsWith('…needle'), shown)
  // A match inside a long word keeps that word, even when the lead falls inside it.
  assert.match(show('one two three four five verylongneedleword and more text to fill the line', 'needle', 40), /verylongneedleword/)
})
