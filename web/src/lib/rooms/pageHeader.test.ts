import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { visibleTabs } from './workViews.ts'
import { computerLabel } from '../computers/computers.ts'

const read = (p: string): string => readFileSync(new URL(p, import.meta.url), 'utf8')

test('Questions, Runs and Computers use the shared TabHeader', () => {
  for (const p of ['./Questions.svelte', '../workflows/Runs.svelte', '../computers/Computers.svelte']) {
    assert.match(read(p), /<TabHeader /, p)
  }
  assert.doesNotMatch(read('../workflows/Runs.svelte'), /class="glyph"|class="tally"/)
})

test('TabHeader renders an optional action snippet', () => {
  assert.match(read('./TabHeader.svelte'), /\{@render action\?\.\(\)\}/)
})

test('Runs shows the counts line only when there is a run', () => {
  const s = read('../workflows/Runs.svelte')
  assert.match(s, /list\?\.runs\?\.length \? `\$\{list\.running\} running · \$\{list\.total\} total` : ''/)
})

test('a hidden tab never shows in the header', () => {
  const row = { id: 'runs', label: 'runs', hint: '', tabs: [{ id: 'runs', label: 'Workflows' }, { id: 'jobs', label: 'Shell jobs', hidden: true }] }
  assert.equal(visibleTabs(row), null)
  const files = { id: 'code', label: 'code', hint: '', tabs: [{ id: 'files', label: 'Files' }, { id: 'github', label: 'Pull requests' }] }
  assert.equal(visibleTabs(files)?.length, 2)
  assert.equal(visibleTabs(undefined), null)
})

test('the room root computer is labelled apart from the root session', () => {
  assert.equal(computerLabel({ session: 'root-dyna-agent', name: 'root' }), 'root (room)')
  assert.equal(computerLabel({ session: 'ses_1', name: 'root' }), 'root')
  assert.equal(computerLabel({ session: 'ses_2', name: 'page-header' }), 'page-header')
})
