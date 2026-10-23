import test from 'node:test'
import assert from 'node:assert/strict'
import { changeLines, noteGroups, sameChanges } from './notices.ts'

const notice = (lines: string): string => `[DYNA INSTRUCTIONS CHANGED] Since this conversation started, your standing instructions changed. These changes apply now:\n${lines}`

test('changeLines names each changed section in one line, not its full text', () => {
  const text = notice([
    '- Session rules: now:\n[DYNA SESSION]\n- Posts to @root arrive as your turn input.\n- `sessions`: session lifecycle and dormancy.',
    '- Room rules: added "Keep every PR small and send screenshots."; removed "Old rule".',
    '- Goal and brief: removed.',
  ].join('\n'))
  assert.deepEqual(changeLines(text), [
    'Session rules changed',
    'Room rules: added "Keep every PR small and send screenshots."; removed "Old rule"',
    'Goal and brief removed',
  ])
  assert.deepEqual(changeLines('plain text'), [])
})

test('noteGroups merges a run of Dyna notices and leaves other rows alone', () => {
  const row = (kind: string, id: string) => ({ key: id, post: { id, kind, to: [`r:${id}`], text: notice('- Session rules: now:\nx'), ts: 1 } })
  const rows = [row('message', 'm1'), row('instructions', 'a'), row('instructions', 'b'), row('message', 'm2'), row('instructions', 'c')]
  const groups = noteGroups(rows, 'r')
  assert.deepEqual([...groups].map(([i, g]) => [i, g && g.map((n) => n.target)]), [[1, ['a', 'b']], [2, null], [4, ['c']]])
})

test('sameChanges lists sessions with identical changes once', () => {
  const n = (target: string, lines: string) => ({ id: target, target, text: `[DYNA INSTRUCTIONS CHANGED] x\n${lines}` })
  const both = '- Session rules: now:\nbody\n- Room rules: now:\nbody'
  const groups = sameChanges([n('lead', both), n('cpu', '- Room rules: now:\nbody'), n('wakes', both)])
  assert.deepEqual(groups.map((g) => [g.targets, g.lines]), [[['lead', 'wakes'], ['Session rules changed', 'Room rules changed']], [['cpu'], ['Room rules changed']]])
})

test('sameChanges gives each group a unique key, even for one session with two notices', () => {
  const n = (id: string, lines: string) => ({ id, target: 'lead', text: `[DYNA INSTRUCTIONS CHANGED] x\n${lines}` })
  const groups = sameChanges([n('n1', '- Room rules: now:\nbody'), n('n2', '- Goal and brief: now:\nbody')])
  assert.equal(groups.length, 2)
  assert.deepEqual(groups.map((g) => g.key), ['n1', 'n2'])
})

test('seat notices stay separate and carry their label and handoff body', () => {
 const row = (kind: string, id: string, text: string) => ({ post: { kind, id, text, to: ['alpha:root'], ts: 1 } })
 const rows = [row('instructions', 'i', notice('- Room rules: now:\nx')), row('seat', 'a', 'codex (external) took the root seat'), row('seat', 'b', 'Internal root took back the seat\n\n[ROOT SEAT] context\n\n[ROOT SEAT HANDOFF] The external orchestrator left this note:\n\nPR 12 waits for CI.'), row('instructions', 'j', notice('- Room rules: now:\ny'))]
 const groups = noteGroups(rows, 'alpha')
 assert.deepEqual([...groups.values()].map((g) => g?.length), [1, 1, 1, 1])
 assert.equal(groups.get(1)?.[0]?.label, 'codex (external) took the root seat')
 assert.equal(groups.get(1)?.[0]?.body, '')
 assert.equal(groups.get(2)?.[0]?.body, 'PR 12 waits for CI.')
})

test('an attach handoff preserves quoted detach context in its body', () => {
 const body = '[DYNA ROOT SEAT HANDOFF]\nRecent input:\n[ROOT SEAT HANDOFF] The external orchestrator left this note:\n\nEarlier note.'
 const groups = noteGroups([{ post: { id: 'attach', kind: 'seat', text: 'codex (external) took the root seat\n\n' + body } }], 'alpha')
 assert.equal(groups.get(0)?.[0]?.body, body)
})
