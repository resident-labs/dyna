import test from 'node:test'
import assert from 'node:assert/strict'
import { toolCallLabel } from './toolCallLabel.ts'

// One row says the kind once. Several files use a count title and list every path.
test('a tool row names the kind once and lists every file', () => {
  const readTwo = toolCallLabel({
    step: 'read',
    label: 'pebble-test.png',
    toolName: 'Read',
    tool: 'read 2 files (screenshots/pebble-test.png, screenshots/cow-test.png)',
    target: 'screenshots/pebble-test.png',
  })
  assert.equal(readTwo.title, 'Read 2 files')
  assert.deepEqual(readTwo.paths, ['screenshots/pebble-test.png', 'screenshots/cow-test.png'])
  assert.equal(readTwo.detail, '')
  assert.equal([readTwo.title, readTwo.detail].join(' ').match(/read/gi)?.length, 1)

  const one = toolCallLabel({
    step: 'read',
    label: 'decoder.go',
    toolName: 'Read',
    tool: 'read',
    target: 'decoder/decoder.go',
  })
  assert.equal(one.title, 'decoder.go')
  assert.equal(one.detail, '')
  assert.deepEqual(one.paths, ['decoder/decoder.go'])

  const edit = toolCallLabel({
    step: 'edit',
    label: 'a.go',
    toolName: 'Edit',
    tool: 'edited 3 files (r/a.go, r/b.go, r/c.go)',
    target: 'r/a.go',
  })
  assert.equal(edit.title, 'Edit 3 files')
  assert.equal(edit.detail, '')
  assert.deepEqual(edit.paths, ['r/a.go', 'r/b.go', 'r/c.go'])

  const search = toolCallLabel({
    step: 'search',
    label: 'frameCount',
    toolName: 'Grep',
    tool: 'searched for',
    target: 'frameCount',
  })
  assert.equal(search.title, 'frameCount')
  assert.equal(search.detail, 'Grep')
  assert.deepEqual(search.paths, [])

  const list = toolCallLabel({
    step: 'list',
    label: '*.go',
    toolName: 'list',
    tool: 'listed',
    target: '*.go',
  })
  assert.equal(list.title, '*.go')
  assert.equal(list.detail, '')

  const command = toolCallLabel({
    step: 'run',
    label: 'go',
    toolName: 'command',
    tool: 'ran',
    target: 'go test ./...',
  })
  assert.equal(command.title, 'go')
  assert.equal(command.detail, 'Command')
  assert.equal(command.detail.match(/command/gi)?.length, 1)
  assert.deepEqual(command.paths, ['go test ./...'])

  const wrote = toolCallLabel({
    step: 'write',
    label: 'note.md',
    toolName: 'Write',
    tool: 'wrote',
    target: 'docs/note.md',
  })
  assert.equal(wrote.title, 'note.md')
  assert.equal(wrote.detail, '')
  assert.deepEqual(wrote.paths, ['docs/note.md'])
})
