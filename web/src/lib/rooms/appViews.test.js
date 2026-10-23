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
function find(node, ok) {
  if (!node || typeof node !== 'object') return null
  if (ok(node)) return node
  for (const v of Object.values(node)) {
    const hit = Array.isArray(v) ? v.map((x) => find(x, ok)).find(Boolean) : find(v, ok)
    if (hit) return hit
  }
  return null
}
const chain = find(ast.fragment, (n) => n.type === 'IfBlock' && source.slice(n.test.start, n.test.end) === "view === 'cihealth'")
const components = (node, out = []) => {
  if (!node || typeof node !== 'object') return out
  if (node.type === 'Component') out.push(node.name)
  for (const [k, v] of Object.entries(node)) if (k !== 'alternate' || node.type !== 'IfBlock') Array.isArray(v) ? v.forEach((x) => components(x, out)) : components(v, out)
  return out
}
function rendered(view) {
  for (let n = chain; n; ) {
    if (vm.runInNewContext(source.slice(n.test.start, n.test.end), { view, hasTerminal: true, computers: [] })) return components(n.consequent)
    const next = n.alternate?.nodes?.[0]
    if (next?.type === 'IfBlock' && next.elseif) n = next
    else return components(n.alternate)
  }
}

test('every sidebar tab renders its own page, not the feed', () => {
  assert.ok(chain, 'App view chain not found')
  const expected = { issues: 'GitHub', library: 'Library', services: 'Services' }
  for (const [view, name] of Object.entries(expected)) assert.ok(rendered(view).includes(name), `${view} renders ${rendered(view)}`)
  for (const tab of sections.flatMap((s) => s.rows).flatMap((r) => r.tabs)) assert.ok(!rendered(tab.id).includes('Feed'), `${tab.id} falls through to the feed`)
  assert.ok(rendered('general').includes('Feed'))
  // Rules and Room settings live in the Room settings modal, not in a view.
  for (const view of ['settings', 'rules', 'room-general', 'room-hooks']) assert.deepEqual(rendered(view).filter((name) => name === 'Rules' || name === 'RoomSettings'), [], view)
})
