import test from 'node:test'
import assert from 'node:assert/strict'
import { PREVIEW_SMALL, postPreview } from './preview.ts'

const oneLine = (s: string) => s.replace(/\s+/g, ' ').trim()
// filler adds a paragraph long enough that the hidden tail is worth hiding.
const filler = (n = 4) => 'The rest of the post runs on. '.repeat(n).trim()

test('a first paragraph that fits is the preview', () => {
  const para = 'Rechecked: PR #288 is still open, so there is nothing to rebase yet.'
  const preview = postPreview(`${para}\n\n${filler()}`)
  assert.equal(preview.text, para)
  assert.equal(preview.truncated, true)
})

test('a long paragraph ends at the last whole sentence that fits', () => {
  const text = `${'The first sentence is short. '.repeat(10)}The next sentence is far too long to show in this preview.`
  const preview = postPreview(text, 100)
  assert.equal(preview.truncated, true)
  assert.match(preview.text, /sentence is short\.$/)
  assert.doesNotMatch(preview.text, /far too long/)
})

test('a first sentence over the budget cuts at a word and marks the cut', () => {
  const text = `Rebase Feed.svelte the moment the other branch lands, ${'then run the boundary tests again and again. '.repeat(4)}`
  const preview = postPreview(text, 40)
  assert.equal(preview.truncated, true)
  assert.ok(preview.text.endsWith('…'))
  const head = preview.text.slice(0, -1)
  assert.equal(text.slice(head.length, head.length + 1), ' ', 'the cut follows a whole word and a space')
  assert.ok(preview.text.length <= 41)
})

test('a short hidden tail shows the whole post with no button', () => {
  const head = 'A'.repeat(140)
  const tail = 'B'.repeat(PREVIEW_SMALL - 10)
  const preview = postPreview(`${head} ${tail}`)
  assert.equal(preview.truncated, false)
  assert.equal(preview.text, `${head} ${tail}`)
})

test('markdown blocks stay whole: a code first block is opened and closed', () => {
  const code = ['```ts', 'const one = 1', 'const two = 2', 'const three = 3', 'const four = 4', 'const five = 5', 'const six = 6', 'const seven = 7', '```', '', filler()].join('\n')
  const preview = postPreview(code, 60)
  assert.equal(preview.truncated, true)
  const fences = preview.text.split('\n').filter((line) => line.trim().startsWith('```'))
  assert.equal(fences.length, 2, 'the preview closes the fence it opens')
  assert.equal(fences[0], '```ts')
})

test('markdown blocks stay whole: a long list keeps whole items', () => {
  const text = [
    '- share the helper between the feed and DMs so the boundary rule is the same everywhere',
    '- second item, which is also long enough to need the rest of its own line in the list',
    '- third item',
    '- fourth item',
  ].join('\n')
  const preview = postPreview(text, 60)
  assert.equal(preview.truncated, true)
  for (const line of preview.text.split('\n')) assert.match(line, /^- \S/, 'no line is a bare bullet')
})

test('a code block after prose previews the prose before it', () => {
  const code = ['```', ...Array.from({ length: 12 }, (_, i) => `const step${i + 1} = ${i + 1}`), '```'].join('\n')
  const preview = postPreview(`The fix is small.\n\n${code}`, 30)
  assert.equal(preview.text, 'The fix is small.')
  assert.equal(preview.truncated, true)
})

test('a cut never leaves an inline code tick or a link open', () => {
  const text = `Read the \`Feed.svelte\` file and then the [design doc](https://example.com/design) ${filler(5)}`
  const preview = postPreview(text, 45)
  assert.equal(preview.truncated, true)
  assert.equal((preview.text.split('`').length - 1) % 2, 0)
  assert.equal(preview.text.includes('['), preview.text.includes(']'))
})

test('a short post is not truncated', () => {
  const preview = postPreview('Rechecked: PR #289 is unchanged at d7fd83b81 and still MERGEABLE.')
  assert.equal(preview.truncated, false)
  assert.equal(preview.text, 'Rechecked: PR #289 is unchanged at d7fd83b81 and still MERGEABLE.')
})

test('a table keeps its header rule', () => {
  const text = ['| step | state |', '| --- | --- |', `| one | ${'open '.repeat(30)}|`, '| two | done |'].join('\n')
  const preview = postPreview(text, 30)
  assert.equal(preview.truncated, true)
  const lines = preview.text.split('\n')
  assert.equal(oneLine(lines[0] ?? ''), '| step | state |')
  assert.match(lines[1] ?? '', /^\| --- \| --- \|$/)
})
