import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { sections } from './workViews.ts'

// Walk App's real view chain ({#if view === 'cihealth'} … {:else} Feed) and find
// which component each sidebar tab renders. A tab that falls through to the
// feed is a broken page.
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const ast = parse(source, { modern: true })

// The view chain is a tree of if blocks; only these fields matter to the walk.
interface IfNode {
  type: string
  test: { start: number; end: number }
  consequent: unknown
  alternate?: { nodes?: unknown[] }
  elseif?: boolean
}

function isIfNode(value: unknown): value is IfNode {
  if (!value || typeof value !== 'object') return false
  const node = value as Record<string, unknown>
  const test = node.test
  return typeof node.type === 'string' && typeof node.start === 'number' && typeof node.end === 'number'
    && !!test && typeof test === 'object' && typeof Reflect.get(test, 'start') === 'number'
}

function find(node: unknown, ok: (node: IfNode) => boolean): IfNode | null {
  if (!node || typeof node !== 'object') return null
  if (isIfNode(node) && ok(node)) return node
  for (const v of Object.values(node)) {
    const hit = Array.isArray(v) ? v.map((x) => find(x, ok)).find(Boolean) : find(v, ok)
    if (hit) return hit
  }
  return null
}
const chain = find(ast.fragment, (n) => n.type === 'IfBlock' && source.slice(n.test.start, n.test.end) === "view === 'cihealth'")
const components = (node: unknown, out: string[] = []): string[] => {
  if (!node || typeof node !== 'object') return out
  const record = node as Record<string, unknown>
  if (record.type === 'Component' && typeof record.name === 'string') out.push(record.name)
  for (const [k, v] of Object.entries(record)) if (k !== 'alternate' || record.type !== 'IfBlock') Array.isArray(v) ? v.forEach((x) => components(x, out)) : components(v, out)
  return out
}
function rendered(view: string): string[] {
  for (let n = chain; n; ) {
    if (vm.runInNewContext(source.slice(n.test.start, n.test.end), { view, hasTerminal: true, computers: [] })) return components(n.consequent)
    const next = n.alternate?.nodes?.[0]
    if (isIfNode(next) && next.type === 'IfBlock' && next.elseif) n = next
    else return components(n.alternate)
  }
  return []
}

test('every sidebar tab renders its own page, not the feed', () => {
  assert.ok(chain, 'App view chain not found')
  const expected = { library: 'Library', services: 'Services' }
  for (const [view, name] of Object.entries(expected)) assert.ok(rendered(view).includes(name), `${view} renders ${rendered(view)}`)
  for (const tab of sections.flatMap((s) => s.rows).flatMap((r) => r.tabs)) assert.ok(!rendered(tab.id).includes('Feed'), `${tab.id} falls through to the feed`)
  assert.ok(rendered('general').includes('Feed'))
  // Rules and Room settings live in the Room settings modal, not in a view.
  for (const view of ['settings', 'rules', 'room-general', 'room-hooks']) assert.deepEqual(rendered(view).filter((name) => name === 'Rules' || name === 'RoomSettings'), [], view)
})

// Issues used to be a room tab of its own. It is the review workspace's
// fourth tab now, so there is one Issues page and no separate view chain.
test('Issues is the review workspace tab, not a room view', () => {
  const workspace = readFileSync(new URL('../review/ReviewWorkspace.svelte', import.meta.url), 'utf8')
  assert.match(workspace, /\{ id: 'files', label: 'Files' \}, \{ id: 'changes', label: 'Changes' \}, \{ id: 'prs', label: 'PRs' \}, \{ id: 'issues', label: 'Issues' \}/)
  assert.match(workspace, /\{@render issuesTab\(\)\}/)
  assert.match(source, /\{#snippet issuesTab\(\)\}/)
  assert.doesNotMatch(source, /\{:else if view === 'issues'\}/)
  // The Issues view still opens the workspace on that tab, so old links work.
  assert.match(source, /if \(next === 'issues'\) openReview\('issues'\)/)
  assert.doesNotMatch(source, /roomTabs/)
})
