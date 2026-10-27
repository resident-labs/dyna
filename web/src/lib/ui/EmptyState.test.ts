import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const views = ['rooms/Home', 'requests/Requests', 'goals/Goals', 'rooms/Rules', 'workflows/Runs', 'workflows/Jobs', 'computers/Computers', 'sessions/Session']

test('full-panel views share the EmptyState component', () => {
  for (const v of views) {
    const src = readFileSync(new URL(`../${v}.svelte`, import.meta.url), 'utf8')
    assert.match(src, /import EmptyState from '\.\.\/ui\/EmptyState\.svelte'/, v)
  }
})
