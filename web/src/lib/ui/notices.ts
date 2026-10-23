import type { Post } from '../api/room.ts'
// Dyna notices render as quiet system notes in feeds and DMs.
//
// TODO: legacy/shim remove later — the readers below read the free-text
// instruction and seat notes that posts stored before their families were
// typed. A typed session note now renders through SessionNoteCard, so nothing
// new reaches these parsers. Remove them when retained room history ages out.
import { short } from './format.ts'

// Feed rows and DM notices can carry only part of a stored post.
export type NoticePost = Partial<Pick<Post, 'id' | 'kind' | 'text' | 'to' | 'attachments'>> & {
  ts?: Post['ts'] | string
}

export interface NoteData extends Pick<NoticePost, 'id' | 'text' | 'ts' | 'attachments'> {
  target: string | undefined
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

// isNote reports a stored free-text instruction or seat note.
// TODO: legacy/shim remove later — see the note at the top of this file.
export const isNote = (post: NoticePost | null | undefined): post is NoticePost & { kind: 'instructions' | 'seat' } => ['instructions', 'seat'].includes(post?.kind ?? '')

// Notes of one family fold into one row when they follow each other.
const family = (post: NoticePost | null | undefined) => (post?.kind === 'instructions' ? 'instructions' : '')

// Seat text starts with its display label. Detach retains root input context
// behind that line; only the written handoff belongs in the expanded panel.
// noteData reads the label and body of a stored seat note.
// TODO: legacy/shim remove later — a typed session/seat post carries its label
// in the payload; see the note at the top of this file.
export function noteData(post: NoticePost, target?: string): NoteData {
  const note: NoteData = { id: post.id, target, text: post.text, ts: post.ts, attachments: post.attachments }
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
