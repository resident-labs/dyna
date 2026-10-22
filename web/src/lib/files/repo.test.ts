import test from 'node:test'
import assert from 'node:assert/strict'
import { parseDiff, diffTotals, statusLetter, split, crumbs, parent, treeRows, type RepoEntry, type TreeRow } from './repo.ts'
import { splitLines } from '../ui/highlight.js'

const diff = `diff --git a/a.js b/a.js
index 1..2 100644
--- a/a.js
+++ b/a.js
@@ -1,3 +1,4 @@ function f() {
 one
-two
+TWO
+three
 four
\\ No newline at end of file
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1 @@
+package x
diff --git a/old.txt b/new.txt
similarity index 100%
rename from old.txt
rename to new.txt
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

test('parseDiff reads files, kinds, hunks, line numbers and counts', () => {
  const files = parseDiff(diff)
  assert.deepEqual(files.map((f) => [f.path, f.kind]), [['a.js', 'modified'], ['new.go', 'added'], ['new.txt', 'renamed'], ['img.png', 'binary']])
  const a = files[0]
  assert.ok(a)
  assert.equal(a.adds, 2)
  assert.equal(a.dels, 1)
  assert.equal(a.hunks[0]?.context, 'function f() {')
  assert.deepEqual(a.hunks[0]?.lines.map((l) => [l.t, l.a, l.b]), [[' ', 1, 1], ['-', 2, 0], ['+', 0, 2], ['+', 0, 3], [' ', 3, 4], ['\\', 0, 0]])
  assert.equal(files[2]?.from, 'old.txt')
  assert.equal(files[1]?.hunks[0]?.lines[0]?.b, 1)
})

test('parseDiff of empty text is empty', () => {
  assert.deepEqual(parseDiff(''), [])
  assert.deepEqual(parseDiff(undefined), [])
})

test('statusLetter picks the side and the special states', () => {
  assert.equal(statusLetter({ indexStatus: 'M', worktreeStatus: 'D' }, 'staged'), 'M')
  assert.equal(statusLetter({ indexStatus: 'M', worktreeStatus: 'D' }, 'unstaged'), 'D')
  assert.equal(statusLetter({ untracked: true }, 'untracked'), '?')
  assert.equal(statusLetter({ conflicted: true, status: 'M' }, 'unstaged'), 'U')
  assert.equal(statusLetter({ status: 'X' }), 'M')
})

test('path helpers', () => {
  assert.deepEqual(split('a/b/c.go'), ['a/b/', 'c.go'])
  assert.deepEqual(split('c.go'), ['', 'c.go'])
  assert.deepEqual(crumbs('a/b'), [{ name: 'a', path: 'a' }, { name: 'b', path: 'a/b' }])
  assert.equal(parent('a/b/c'), 'a/b')
  assert.equal(parent('a'), '')
})

test('splitLines closes and reopens spans across line ends', () => {
  assert.deepEqual(splitLines('a<span class="c">/* x\ny */</span>\nb &lt; c'), ['a<span class="c">/* x</span>', '<span class="c">y */</span>', 'b &lt; c'])
  assert.deepEqual(splitLines('<span class="a"><span class="b">x\ny</span></span>'), ['<span class="a"><span class="b">x</span></span>', '<span class="a"><span class="b">y</span></span>'])
  assert.deepEqual(splitLines(''), [''])
})

test('diffTotals sums files, additions and deletions for the Files changed header', () => {
  const files = parseDiff(diff)
  const t = diffTotals(files)
  assert.equal(t.files, files.length)
  assert.equal(t.adds, files.reduce((n, f) => n + f.adds, 0))
  assert.equal(t.dels, files.reduce((n, f) => n + f.dels, 0))
  assert.ok(t.adds > 0 && t.dels > 0)
  assert.deepEqual(diffTotals([]), { files: 0, adds: 0, dels: 0 })
})

test('treeRows keeps the row object of every row an open or close did not change', () => {
  const file = (path: string): RepoEntry => ({ path, name: path.split('/').pop() ?? path, kind: 'file' })
  const dir = (path: string): RepoEntry => ({ path, name: path.split('/').pop() ?? path, kind: 'dir' })
  const lists = { '': { entries: [dir('a'), dir('b'), file('c')] }, a: { entries: [file('a/x')] }, b: { entries: [file('b/y')] } }
  const byPath = (rows: TreeRow[]) => new Map(rows.map((r): [string, TreeRow] => [r.path, r]))

  const first = treeRows(lists, new Set(['', 'a']))
  assert.deepEqual(first.map((r) => [r.path, r.depth, 'expanded' in r && r.expanded]), [['a', 0, true], ['a/x', 1, false], ['b', 0, false], ['c', 0, false]])

  const opened = treeRows(lists, new Set(['', 'a', 'b']), byPath(first))
  assert.deepEqual(opened.map((r) => r.path), ['a', 'a/x', 'b', 'b/y', 'c'])
  const was = byPath(first)
  for (const r of opened) {
    if (r.path === 'b' || r.path === 'b/y') assert.notEqual(r, was.get(r.path), r.path + ' changed')
    else assert.equal(r, was.get(r.path), r.path + ' is unchanged')
  }
})
