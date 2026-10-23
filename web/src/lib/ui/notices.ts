import type { Post } from '../api/room.ts'
// Dyna notices render as quiet system notes in feeds and DMs.
import { short } from './format.ts'

// Feed rows and DM notices can carry only part of a stored post.
export type NoticePost = Partial<Pick<Post, 'id' | 'kind' | 'text' | 'to' | 'attachments'>> & {
  ts?: Post['ts'] | string
}

export interface SlowTest {
  issue: NoticePost['id']
  id: string
  name: string
  time: string
  limit: string
  prs: number[]
  area: string
}

export interface NoteData extends Pick<NoticePost, 'id' | 'text' | 'ts' | 'attachments'> {
  target: string | undefined
  slow?: SlowTest
  label?: string
  body?: string
}

const HEAD = /^\[DYNA INSTRUCTIONS CHANGED\][^\n]*\n?/
// internal/instructions writes one line per changed section:
// "- Name: removed.", "- Name: now:" followed by its full text, or
// "- Name: added "x"; removed "y".".
const SECTION = /^- ([A-Z][\w ]*): (removed\.|now:|(?:added|removed) .*)$/

// changeLines names each changed section in one line, without its full text.
export function changeLines(text?: string | null) {
  if (!text || !HEAD.test(text || '')) return []
  const out = []
  for (const line of text.replace(HEAD, '').split('\n')) {
    const m = line.match(SECTION)
    if (!m) continue
    const [, name, what = ''] = m
    out.push(what === 'now:' ? `${name} changed` : what === 'removed.' ? `${name} removed` : `${name}: ${what.replace(/\.$/, '')}`)
  }
  return out
}

// fullText is the notice without its standard first line.
export const fullText = (text?: string | null) => (text || '').replace(HEAD, '').trim()

const SLOW = 'Slow test over the CI limit: '

// slowTest reads a slow-test issue that the PR watcher files
// (internal/pr/watch/test_budget_scope.go): the title names the test, and
// the note starts with "Time:", "PRs:" and "Area:" lines. An older post
// without those lines keeps only the test.
export function slowTest(post: NoticePost | null | undefined): SlowTest | null {
  if (post?.kind !== 'issue' || !post.text?.startsWith(SLOW)) return null
  const [title = '', ...rest] = post.text.split('\n')
  const id = title.slice(SLOW.length).trim()
  const fact = (key: string) => rest.find((l) => l.startsWith(key + ': '))?.slice(key.length + 2).trim() ?? ''
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

export const isNote = (post: NoticePost | null | undefined): post is NoticePost & { kind: 'instructions' | 'seat' | 'issue' } => ['instructions', 'seat'].includes(post?.kind ?? '') || !!slowTest(post)

// Notes of one family fold into one row when they follow each other.
const family = (post: NoticePost | null | undefined) => (slowTest(post) ? 'slow' : post?.kind === 'instructions' ? 'instructions' : '')

// Seat text starts with its display label. Detach retains root input context
// behind that line; only the written handoff belongs in the expanded panel.
export function noteData(post: NoticePost, target?: string): NoteData {
  const note: NoteData = { id: post.id, target, text: post.text, ts: post.ts, attachments: post.attachments }
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
export function noteGroups(rows: readonly { post?: NoticePost | null }[], roomName?: string) {
  const groups = new Map<number, NoteData[] | null>()
  for (let i = 0; i < rows.length; i++) {
    const post = rows[i]?.post
    if (!isNote(post)) continue
    const kin = family(post)
    if (kin && family(rows[i - 1]?.post) === kin) { groups.set(i, null); continue }
    const run = []
    for (let j = i; ; j++) {
      const p = rows[j]?.post
      if (!isNote(p) || !(j === i || kin && family(p) === kin)) break
      run.push(noteData(p, short(p.to?.[0] || '', roomName)))
    }
    groups.set(i, run)
  }
  return groups
}

// sameChanges groups notes whose sessions got the same changes, so a rule
// change that reaches every session reads as one list of names.
export function sameChanges<N extends Pick<NoteData, 'id' | 'target' | 'text'>>(notes: readonly N[]) {
  const groups = new Map<string, { key: N['id']; targets: N['target'][]; lines: string[]; notes: N[] }>()
  for (const n of notes) {
    const lines = changeLines(n.text)
    const key = lines.join('\n')
    let group = groups.get(key)
    if (!group) {
      group = { key: n.id, targets: [], lines, notes: [] }
      groups.set(key, group)
    }
    group.targets.push(n.target)
    group.notes.push(n)
  }
  return [...groups.values()]
}
