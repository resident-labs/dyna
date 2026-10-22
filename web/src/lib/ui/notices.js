// Dyna notices render as quiet system notes in feeds and DMs.
import { short } from './format.js'

const HEAD = /^\[DYNA INSTRUCTIONS CHANGED\][^\n]*\n?/
// internal/instructions writes one line per changed section:
// "- Name: removed.", "- Name: now:" followed by its full text, or
// "- Name: added "x"; removed "y".".
const SECTION = /^- ([A-Z][\w ]*): (removed\.|now:|(?:added|removed) .*)$/

// changeLines names each changed section in one line, without its full text.
export function changeLines(text) {
  if (!HEAD.test(text || '')) return []
  const out = []
  for (const line of text.replace(HEAD, '').split('\n')) {
    const m = line.match(SECTION)
    if (!m) continue
    const [, name, what] = m
    out.push(what === 'now:' ? `${name} changed` : what === 'removed.' ? `${name} removed` : `${name}: ${what.replace(/\.$/, '')}`)
  }
  return out
}

// fullText is the notice without its standard first line.
export const fullText = (text) => (text || '').replace(HEAD, '').trim()

const SLOW = 'Slow test over the CI limit: '

// slowTest reads a slow-test issue that the PR watcher files
// (internal/pr/watch/test_budget_scope.go): the title names the test, and
// the note starts with "Time:", "PRs:" and "Area:" lines. An older post
// without those lines keeps only the test.
export function slowTest(post) {
  if (post?.kind !== 'issue' || !post.text?.startsWith(SLOW)) return null
  const [title, ...rest] = post.text.split('\n')
  const id = title.slice(SLOW.length).trim()
  const fact = (key) => rest.find((l) => l.startsWith(key + ': '))?.slice(key.length + 2).trim() ?? ''
  const [time = '', limit = ''] = fact('Time').split(', limit ')
  const at = id.indexOf('/Test')
  return {
    issue: post.id, id,
    name: at >= 0 ? id.slice(at + 1) : id.replace(/^test\//, ''),
    time, limit,
    prs: fact('PRs').split(', ').map((n) => Number(n.replace('#', ''))).filter(Boolean),
    area: fact('Area'),
  }
}

export const isNote = (post) => ['instructions', 'seat'].includes(post?.kind) || !!slowTest(post)

// Notes of one family fold into one row when they follow each other.
const family = (post) => (slowTest(post) ? 'slow' : post?.kind === 'instructions' ? 'instructions' : '')

// Seat text starts with its display label. Detach retains root input context
// behind that line; only the written handoff belongs in the expanded panel.
export function noteData(post, target) {
  const note = { id: post.id, target, text: post.text, ts: post.ts, attachments: post.attachments }
  const slow = slowTest(post)
  if (slow) note.slow = slow
  if (post.kind === 'seat') {
    const [label, ...rest] = (post.text || '').split('\n')
    const text = rest.join('\n').trim()
    const marker = '[ROOT SEAT HANDOFF] The external orchestrator left this note:'
    note.label = label
    const detach = label === 'Internal root took back the seat'
    note.body = detach && text.includes(marker) ? text.slice(text.indexOf(marker) + marker.length).trim() : detach && text.startsWith('[ROOT SEAT]') ? '' : text
  }
  return note
}

// noteGroups maps each feed row index that starts a run of notices to the
// run's notes, and each later row of the run to null (it is shown in the run).
export function noteGroups(rows, roomName) {
  const groups = new Map()
  for (let i = 0; i < rows.length; i++) {
    if (!isNote(rows[i]?.post)) continue
    const kin = family(rows[i].post)
    if (kin && family(rows[i - 1]?.post) === kin) { groups.set(i, null); continue }
    const run = []
    for (let j = i; isNote(rows[j]?.post) && (j === i || kin && family(rows[j].post) === kin); j++) {
      const p = rows[j].post
      run.push(noteData(p, short(p.to?.[0] || '', roomName)))
    }
    groups.set(i, run)
  }
  return groups
}

// sameChanges groups notes whose sessions got the same changes, so a rule
// change that reaches every session reads as one list of names.
export function sameChanges(notes) {
  const groups = new Map()
  for (const n of notes) {
    const lines = changeLines(n.text)
    const key = lines.join('\n')
    if (!groups.has(key)) groups.set(key, { key: n.id, targets: [], lines, notes: [] })
    groups.get(key).targets.push(n.target)
    groups.get(key).notes.push(n)
  }
  return [...groups.values()]
}
