import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { bestProfile, gaps, stateLabel, supportBrief, supportChip, supportDraft, supportTitle } from './support.ts'

const item = (key: string, label: string, state: string, reason = '', chip = '') => ({ key, label, state, reason, chip })
const full = { dialect: 'claude-stream-json', partial: false, items: [item('transcript', 'Transcript', 'yes'), item('usage', 'Token counts', 'yes')] }
const muse = { dialect: 'muse-jsonl', partial: true, items: [item('transcript', 'Transcript', 'yes'), item('usage', 'Token counts', 'unavailable', 'Muse JSON output does not report provider usage', 'No token counts')] }
const text = { dialect: 'text', partial: true, items: [item('toolEvents', 'Tool calls', 'no', 'The plain text output does not show tool calls', 'No tool calls'), item('usage', 'Token counts', 'no', 'Plain text output does not report usage', 'No token counts')] }

test('full support shows no chip', () => {
  assert.equal(supportChip({ support: full }), '')
  assert.deepEqual(gaps({ support: full }), [])
  assert.equal(supportChip({}), '')
})

test('partial support shows one short chip', () => {
  assert.equal(supportChip({ support: muse }), 'No token counts')
  assert.equal(supportChip({ support: text }), 'No tool calls +1')
  assert.match(supportTitle({ support: muse }), /Token counts: Muse JSON output does not report provider usage/)
})

test('states read as plain words', () => {
  assert.deepEqual(['yes', 'no', 'unavailable'].map(stateLabel), ['Yes', 'No', 'Unavailable'])
})

test('the support profile follows the reviewer rule', () => {
  const p = (name: string, taste: number, cost: number, intelligence: number, disabled = false) => ({ name, taste, cost, intelligence, disabled })
  assert.equal(bestProfile([p('a', 5, 5, 9), p('b', 7, 4, 1), p('c', 10, 10, 1, true)]), 'b')
  assert.equal(bestProfile([p('b', 5, 5, 9), p('a', 5, 5, 9)]), 'a')
  assert.equal(bestProfile([p('x', 5, 5, 3), p('y', 5, 5, 8)]), 'y')
  assert.equal(bestProfile([]), '')
})

test('the brief names the harness, the gaps and every step', () => {
  const brief = supportBrief({ name: 'wonder', harness: 'muse', support: muse })
  assert.match(brief, /muse harness\. Profile: wonder\. Output dialect: muse-jsonl/)
  assert.match(brief, /Token counts: unavailable\. Muse JSON output does not report provider usage\./)
  assert.match(brief, /real output sample/)
  assert.match(brief, /internal\/workeroutput/)
  assert.match(brief, /conformance test/)
  assert.match(brief, /Open a PR/)
  assert.doesNotMatch(brief, /Transcript: yes/)
})

test('the draft starts a session on the chosen profile', () => {
  assert.equal(supportDraft('sol', '  brief\n'), '/sol brief')
})

test('the list, the editor and the dialog use these helpers', () => {
  const read = (f: string): string => readFileSync(new URL(f, import.meta.url), 'utf8')
  assert.match(read('./ProfilesPane.svelte'), /healthChip\(p\)/)
  assert.match(read('./ProfileEditor.svelte'), /Harness support/)
  assert.match(read('./ProfileEditor.svelte'), /Ask an agent to add support/)
  assert.match(read('./SupportBrief.svelte'), /supportDraft/)
  assert.match(read('./SupportBrief.svelte'), /api\.send\(/)
})
