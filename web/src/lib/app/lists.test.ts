import test from 'node:test'
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { need } from '../../../test/need.ts'
import type * as Lists from './lists.svelte.ts'
import type { Goal } from '../api/goal.ts'

const built = await build({
  entryPoints: [fileURLToPath(new URL('./lists.svelte.ts', import.meta.url))],
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'],
  logLevel: 'silent', plugins: [svelteEsbuildPlugin()],
})
const source = need(built.outputFiles[0]).text
const lists: typeof Lists = await import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'))

test('a late older page cannot replace the current room or its streamed row', async () => {
  let room = 'a', visit = 1
  let resolvePage: (page: Lists.ListPage<Goal>) => void = () => { throw new Error('missing request') }
  const pending = new Promise<Lists.ListPage<Goal>>((resolve) => { resolvePage = resolve })
  const goal = (id: string, text: string): Goal => ({ id, room, text, setter: '', holders: null, shared: false, state: 'active', createdAt: '', updatedAt: '', edits: null })
  let shown: Goal[] = []
  lists.bindLists({
    current: () => ({ room, visit }),
    patch: async (_kind, load) => { await load(() => true) },
    goals: () => pending,
    changes: async () => ({ items: [] }),
    jobs: async () => {},
    setGoals: (rows) => { shown = rows },
    setChanges() {},
    error: (message) => { throw new Error(message) },
  })
  lists.applyListPage('goals', { items: [goal('a', 'old')], older: true, cursor: 'next' })
  const loading = lists.loadOlderLists(['goals'])
  room = 'b'; visit++
  lists.listView.clear()
  shown = lists.applyListPage('goals', { items: [goal('b', 'before')] })
  shown = lists.applyListItem('goals', { op: 'upsert', id: 'b', item: goal('b', 'streamed') }, shown)
  resolvePage({ items: [goal('old-a', 'stale')], older: false })
  await loading
  assert.deepEqual(shown.map((row) => [row.id, row.text]), [['b', 'streamed']])
  assert.deepEqual(lists.listView.pages.goals?.items?.map((row) => [row.id, row.text]), [['b', 'streamed']])
})
