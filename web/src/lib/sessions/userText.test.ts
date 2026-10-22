import test from 'node:test'
import assert from 'node:assert/strict'
import { userText } from './userText.ts'

const block = 'Attachments (2), saved with the room:\n- shot.png · image/png · 12.0 KB · /d/f/a1.png\n- notes.txt · text/plain · 1.0 KB · /d/f/a2.txt\nOpen each file with your file-reading tool. To see an image you need a tool that reads image files.\nIf a path does not open: dyna attachment get r <id> prints it, and -o - writes the bytes.'

test('userText keeps only what the owner typed and lists the files', () => {
  assert.deepEqual(userText(`Fix the coupon bug\n\n${block}`), { text: 'Fix the coupon bug', files: ['shot.png', 'notes.txt'], items: [] })
})

test('userText handles an attachment-only prompt and the short form', () => {
  assert.deepEqual(userText(block).text, '')
  assert.deepEqual(userText('Look at this\n\nAttachments (3): dyna room read r post_1'), { text: 'Look at this', files: [], items: [] })
})

test('userText leaves plain text alone', () => {
  assert.deepEqual(userText('  hello\nworld '), { text: 'hello\nworld', files: [], items: [] })
  assert.deepEqual(userText('See Attachments (2) in the doc'), { text: 'See Attachments (2) in the doc', files: [], items: [] })
  assert.deepEqual(userText(undefined), { text: '', files: [], items: [] })
})

const id1 = '225353899c144463bce35c27'
const id2 = 'aaaaaaaaaaaaaaaaaaaaaaaa'
const real = `Attachments (3), saved with the room:\n- home-design.md · text/markdown · 6.1 KB · /home/a/rooms/r/files/${id1}.txt\n- shot.png · image/png · 2.0 MB · dyna attachment get r ${id2}\n- card.html · HTML card "Plan" · 900 B · /f/${id2}.bin\n- +2 more: dyna room read r post_1\nOpen each file with your file-reading tool.\nIf a path does not open: dyna attachment get r <id> prints it, and -o - writes the bytes.`

test('userText parses stored files into attachment items and hides the agent lines', () => {
  const t = userText(`Plan is ready.\n\n${real}`)
  assert.equal(t.text, 'Plan is ready.')
  assert.deepEqual(t.items, [
    { id: id1, name: 'home-design.md', mime: 'text/markdown', size: 6246 },
    { id: id2, name: 'shot.png', mime: 'image/png', size: 2097152 },
    { id: id2, name: 'card.html', mime: 'text/html', render: 'html', title: 'Plan', size: 900 },
  ])
  assert.doesNotMatch(t.text, /file-reading|dyna attachment/)
})

test('userText drops a list line whose file has no ID', () => {
  assert.deepEqual(userText('Attachments (1), saved with the room:\n- x.txt · text/plain · 1.0 KB · /tmp/x.txt').items, [])
})
