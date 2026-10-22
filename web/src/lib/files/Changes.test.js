import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { setImmediate } from 'node:timers/promises'
import { parse } from 'svelte/compiler'
import { parseDiff } from './repo.ts'

const source = readFileSync(new URL('./Changes.svelte', import.meta.url), 'utf8')
const code = parse(source).instance.content.body.filter((n) => n.type === 'FunctionDeclaration' && ['load', 'fetchUntracked'].includes(n.id.name)).map((n) => source.slice(n.start, n.end)).join('\n')
const diff = (text) => ({ text: ['diff --git a/new.txt b/new.txt', 'new file mode 100644', '--- /dev/null', '+++ b/new.txt', '@@ -0,0 +1 @@', '+' + text, ''].join('\n') })
function app(preview) {
  const calls = []
  const c = { parseDiff, roomId: 'r', session: '', status: null, extra: {}, open: new Set(), loadVersion: 0,
    api: {
      gitStatus: async () => ({ files: [{ path: 'new.txt', untracked: true }] }),
      gitDiff: async (_room, _session, path) => { if (!path) return { text: '' }; calls.push(path); return preview() },
    },
  }
  Object.defineProperty(c, 'groups', { get: () => [{ id: 'untracked', files: c.status?.files ?? [] }] })
  vm.createContext(c)
  vm.runInContext(code, c)
  return { c, calls }
}
const text = (c) => c.extra['new.txt']?.hunks?.[0]?.lines?.[0]?.text

test('Reload refreshes an open untracked file preview', async () => {
  let version = 'first'
  const { c, calls } = app(() => diff(version))
  await c.load(); await setImmediate()
  assert.equal(text(c), 'first')
  version = 'second'
  await c.load(); await setImmediate()
  assert.equal(text(c), 'second')
  assert.deepEqual(calls, ['new.txt', 'new.txt'])
})

test('a preview from an older refresh cannot replace the new contents', async () => {
  let resolve
  const pending = new Promise((yes) => { resolve = yes })
  let first = true
  const { c } = app(() => { if (first) { first = false; return pending }; return diff('current') })
  await c.load()
  await c.load(); await setImmediate()
  assert.equal(text(c), 'current')
  resolve(diff('old')); await setImmediate()
  assert.equal(text(c), 'current')
})
