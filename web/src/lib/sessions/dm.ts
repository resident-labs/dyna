import type { Member, Post } from '../api/room.ts'
import type { Item, Turn, Usage } from '../api/session.ts'
import type { ShellJob } from '../api/terminal.ts'
import { isNote, noteData } from '../ui/notices.ts'
import { userText } from './userText.ts'
import { duration, short, tokens } from '../ui/format.ts'
import { FOLD_AT, quiet, type Finished } from './shellBlock.ts'

type Time = string | number

// A queued message as the DM reads it. The engine's QueueItem has all of
// these; ts is unix milliseconds there.
export interface Receipt {
  id: string
  from: string
  state: string
  ts: Time
  text?: string
  human?: boolean
  cancellable?: boolean
  turn?: number
}

// A transcript item and turn as the DM reads them. A page holds the whole
// Item and Turn; each field here is one the DM uses.
export type DmItem = Pick<Item, 'kind'> & Partial<Item>
export interface DmTurn extends Pick<Turn, 'n' | 'started'>, Partial<Omit<Turn, 'n' | 'started' | 'items'>> {
  items?: DmItem[] | null
}

// What a post row or a thread chip holds of a post: a stored post, or a
// message that only the transcript or the queue knows.
export interface ThreadPost extends Omit<Partial<Post>, 'ts'> {
  id: string
  ts?: Time
  identified?: boolean
  delivery?: string
  pending?: Receipt
}

// One thread of the DM: the posts to or from one other agent.
export interface Thread {
  key: string
  posts: ThreadPost[]
}

// The note a row carries, such as a slow-test issue or a changed instruction.
export interface DmNote {
  id?: string
  target?: string
  text?: string
  ts?: Time
  attachments?: Post['attachments']
  slow?: { name: string }
  label?: string
  body?: string
}

// What every row has. A transcript item's own fields ride along on message,
// tool and thought rows.
interface RowFields extends Omit<Partial<Item>, 'ts' | 'kind'> {
  key: string
  ts?: Time
  author?: string
  human?: boolean
  itemKind?: string
  n?: number
  delivery?: string
  cont?: boolean
}
export interface TurnRow extends RowFields {
  kind: 'turn'
  turn: DmTurn
  wokeBy?: ShellJob
}
export interface TurnEndRow extends RowFields {
  kind: 'turnEnd'
  turn: DmTurn
}
export interface MessageRow extends RowFields {
  kind: 'message'
  post?: ThreadPost
  pending?: Receipt
  quietReceipt?: boolean
  ownerAttachments?: boolean
  attachmentOnly?: boolean
  attachmentPrompt?: string
  folded?: boolean
  threads?: string[]
  suppressMessage?: boolean
}
export interface VoiceRow extends RowFields {
  kind: 'voice'
  post?: ThreadPost
}
export interface ThreadRow extends RowFields {
  kind: 'thread'
  thread: Thread
  posts: ThreadPost[]
  queued: boolean
  shell?: string
}
// One call of the agent's tools; adjacent calls fold into an activity row.
export interface ToolRow extends RowFields {
  kind: 'tool'
}
export interface ActivityRow extends RowFields {
  kind: 'activity'
  items: ToolRow[]
}
export interface ShellRow extends RowFields {
  kind: 'shell'
  job: ShellJob
  waits: number
  finished?: Finished
}
export interface ShellGroupRow extends RowFields {
  kind: 'shellGroup'
  jobs: ShellJob[]
}
export interface ShellNoteRow extends RowFields {
  kind: 'shellNote'
  job: ShellJob
}
export interface InstructionsRow extends RowFields {
  kind: 'instructions'
  note: DmNote
  notes?: DmNote[]
}
// A transcript item that shows as it is: a thought, an error, a note or a compaction divider.
export interface ItemRow extends RowFields {
  kind: 'thought' | 'error' | 'note' | 'compaction'
}
// One row of the DM.
export type DmRow = TurnRow | TurnEndRow | MessageRow | VoiceRow | ThreadRow | ActivityRow | ShellRow | ShellGroupRow | ShellNoteRow | InstructionsRow | ItemRow
// A post waiting to become a row or a chip.
interface PostEvent extends RowFields {
  kind: 'post'
  post: ThreadPost
  threads: string[]
  suppressMessage?: boolean
}
type DmEvent = DmRow | ToolRow | PostEvent

export interface DmView {
  address: string
  threads: Thread[]
  rows: DmRow[]
}
// Session posts form one thread for each other agent, regardless of turn.
// Statuses of a finished turn.
export const done = ['ok', 'error', 'stopped']
const time = (ts?: Time | null): number => Math.max(0, (typeof ts === 'number' ? ts : Date.parse(ts ?? '')) || 0)
export const sessionAddress = (session: { address?: string; member?: Partial<Pick<Member, 'address' | 'name'>> } | null | undefined, room: { name: string }): string => session?.address || session?.member?.address || `${room.name}:${session?.member?.name || ''}`
// postedTo is the "posted to" chip of a DM row. Every row of the root conversation is to root (or the
// owner), so there it says nothing and is empty.
export function postedTo(post: Pick<ThreadPost, 'to'>, address: string, roomName: string): string {
  const to = post.to || []
  if (address === roomName + ':root' && to.every((a) => a === roomName || a === address)) return ''
  return `#${roomName} · ${to.map((a) => '@' + short(a, roomName)).join(', ')}`
}
// threadKeys are the agents a post belongs to as threads. In the root conversation the room's own
// non-human posts (an external root posts as the room) are root steering agents, so they are threads too.
export function threadKeys(post: Pick<ThreadPost, 'from' | 'to' | 'owner' | 'human'>, address: string, owner: string): string[] {
  if (address === owner + ':root' && post.from === owner && !post.human) {
    const to = (post.to || []).filter((a) => a !== owner && a !== address)
    if (to.length) return [...new Set(to)]
  }
  if (post.owner || post.from === owner) return []
  return [...new Set(post.from === address ? (post.to || []).filter((a) => a !== owner && a !== address) : [post.from])].flatMap((key) => (key ? [key] : []))
}

// voiceRequest reports whether post is Voice's request on the owner's behalf:
// a post from the room's voice call that is not a line of the call itself.
export function voiceRequest(post: Pick<ThreadPost, 'from' | 'kind'>, roomName: string): boolean {
  return post.from === `${roomName}:voice-call` && post.kind !== 'voice'
}

export function activitySummary(items: Pick<ToolRow, 'tool' | 'failed' | 'ts'>[], end?: Time): string {
  let read = 0, ran = 0, edited = 0, searched = 0, other = 0, failed = 0
  for (const it of items) {
    const verb = (it.tool || '').toLowerCase()
    const n = Number(verb.match(/\b(\d+) (?:files|patterns)\b/)?.[1] || 1)
    if (/^read/.test(verb)) read += n
    else if (/^ran|^run|^exec|^bash|^shell/.test(verb)) ran += n
    else if (/^edit|^wrote|^write|^patch/.test(verb)) edited += n
    else if (/^search|^listed/.test(verb)) searched += n
    else other += n
    if (it.failed) failed++
  }
  const start = time(items[0]?.ts), stop = Math.max(start, time(end || items.at(-1)?.ts))
  return [stop - start >= 1000 && `Worked for ${duration(start, stop)}`, read && `read ${read} file${read === 1 ? '' : 's'}`, ran && `ran ${ran} command${ran === 1 ? '' : 's'}`, edited && `edited ${edited} file${edited === 1 ? '' : 's'}`, searched && `searched ${searched} time${searched === 1 ? '' : 's'}`, other && `${other} other call${other === 1 ? '' : 's'}`, failed && `${failed} failed`].filter(Boolean).join(' · ')
}

// foldShell turns a call of Dyna's shell tool into a terminal row, and folds a
// wait or a stop into the row of its job. It reports whether the event was
// taken; any other call, and one whose job is not on this page, stays an
// ordinary activity line.
function foldShell(rows: DmRow[], shells: Map<string, ShellRow>, event: ToolRow, address: string): boolean {
  if (event.jobOp === 'run' && event.job) {
    const row: ShellRow = { kind: 'shell', author: address, key: `shell:${event.job.id}`, ts: event.ts, job: event.job, waits: 0 }
    shells.set(event.job.id, row)
    rows.push(row)
    return true
  }
  const row = event.jobId ? shells.get(event.jobId) : undefined
  if (!row) return false
  row.waits++
  return true
}

// How the end of a job reached the agent: the turn a notice opened, if any.
interface Wake {
  n: number
  woke: boolean
}

// foldNotice puts a job's end notice on the block of the call that started the
// job. A notice that woke the agent is named on its turn's rule; one that
// arrived mid-turn leaves a short pointer where it landed.
function foldNotice(rows: DmRow[], block: ShellRow, post: ThreadPost, wake: Wake | undefined, wokeBy: Map<number, ShellJob>, address: string): void {
  block.finished = { at: post.ts ?? 0, turn: wake?.n, woke: !!wake?.woke }
  if (wake?.woke) wokeBy.set(wake.n, block.job)
  else rows.push({ kind: 'shellNote', author: address, key: `note:${post.id}`, ts: post.ts, job: block.job })
}

// foldQuietShells folds each run of FOLD_AT or more quiet shell rows into one
// row. Only a job that is already over and succeeded folds, so a fold never
// hides a job that later fails, and a running one stays in view.
function foldQuietShells(rows: DmRow[]): DmRow[] {
  const out: DmRow[] = []
  let run: ShellRow[] = []
  const flush = () => {
    const first = run[0]
    if (first && run.length >= FOLD_AT) out.push({ kind: 'shellGroup', author: first.author, key: `shells:${first.job.id}`, ts: first.ts, jobs: run.map((r) => r.job) })
    else out.push(...run)
    run = []
  }
  for (const row of rows) {
    if (row.kind === 'shell' && quiet(row.job)) run.push(row)
    else {
      flush()
      out.push(row)
    }
  }
  flush()
  return out
}

// The fields of a row that grouping reads.
interface GroupFields extends Pick<RowFields, 'ts' | 'author' | 'human' | 'itemKind'> {
  kind: string
  key?: string
  ownerAttachments?: boolean
  attachmentOnly?: boolean
  attachmentPrompt?: string
}
export function groupMessages<R extends GroupFields>(rows: R[]): (R & { cont?: boolean })[] {
  let prev: R | null = null
  return rows.map((row, i) => {
    if (row.kind === 'turn' || row.kind === 'turnEnd') prev = null
    if (row.kind !== 'message' && !(['activity', 'thought', 'shell', 'shellGroup', 'shellNote'].includes(row.kind) && row.author)) return row
    const delta = time(row.ts) - time(prev?.ts)
    const ownerAttachment = row.ownerAttachments || row.attachmentOnly
    const adjacent = rows[i - 1]
    const cont = ownerAttachment
      ? !!adjacent && adjacent.kind === 'message' && adjacent.key === row.attachmentPrompt && adjacent.author === row.author && ['prompt', 'steering'].includes(adjacent.itemKind ?? '') && Math.abs(time(row.ts) - time(adjacent.ts)) <= 2 * 60 * 1000
      : !!prev && prev.author === row.author && !!prev.human === !!row.human && delta >= 0 && delta < 7 * 60 * 1000
    prev = row
    return { ...row, cont }
  })
}

// The session fields the DM reads; Session has them all.
export interface DmSession {
  address?: string
  member?: Partial<Pick<Member, 'address' | 'name'>>
  posts?: ThreadPost[] | null
  turns?: DmTurn[] | null
}
// A message that only the transcript or the queue knows: another agent's input.
interface PeerItem {
  id: string
  identified: boolean
  from: string
  to: string[]
  ts: Time
  text: string
}

const isMessage = (row: DmRow): row is MessageRow => row.kind === 'message'
const ITEM_KINDS = ['thought', 'error', 'note', 'compaction'] as const
const isItemKind = (kind: string): kind is ItemRow['kind'] => (ITEM_KINDS as readonly string[]).includes(kind)

// show hides reasoning or compaction dividers when set to false.
export function dmView(session: DmSession | null | undefined, room: { name: string }, show: { reasoning?: boolean; compaction?: boolean } = {}, queue: Receipt[] = []): DmView {
  const address = sessionAddress(session, room)
  const events: DmEvent[] = [], threads = new Map<string, Thread>(), peerItems = new Map<string, PeerItem>(), wakes = new Map<string, Wake>()
  const posts = new Map<string, ThreadPost>((session?.posts || []).map((p): [string, ThreadPost] => [p.id, p]))
  const receipts = new Map<string, Receipt>(queue.filter((q) => ['waiting', 'delivering', 'delivered'].includes(q.state)).map((q): [string, Receipt] => [q.id, q]))
  const matched = new Set<string>(), delivered = new Map<string, { delivery: string; pending?: Receipt }>()
  const threadFor = (key: string): Thread => {
    let thread = threads.get(key)
    if (!thread) { thread = { key, posts: [] }; threads.set(key, thread) }
    return thread
  }
  // Only the owner and this session speak inline; every other agent is a thread.
  // Voice's requests are the owner's words, so they stay inline as voice rows.
  const voice = `${room.name}:voice-call`
  const peer = (from: string, human?: boolean) => !human && from !== room.name && from !== address && from !== voice
  for (const t of session?.turns || []) {
    events.push({ kind: 'turn', key: `turn:${t.n}`, ts: t.started, turn: t })
    if (done.includes(t.status ?? '') && t.ended) events.push({ kind: 'turnEnd', key: `turn-end:${t.n}`, ts: t.ended, turn: t })
    if (t.omitted) events.push({ kind: 'note', key: `omitted:${t.n}`, ts: t.started, text: 'Earlier events of this turn were left out.' })
    for (const [i, it] of (t.items || []).entries()) {
      if (it.kind === 'thought' && show.reasoning === false) continue
      if (it.kind === 'compaction' && show.compaction === false) continue
      const input = ['prompt', 'steering'].includes(it.kind)
      const author = input ? (it.from || room.name).replace(/\s*\(human\)$/, '') : address
      // The time an item shows: its own, or its turn's start when it has none.
      const at = it.ts && time(it.ts) ? it.ts : t.started
      // Match the stored post ID; transcript text can be framed or clipped.
      const receipt = it.postId ? receipts.get(it.postId) : undefined
      const post = it.postId ? posts.get(it.postId) : undefined
      // Another agent's input is that agent's thread, in every delivery state.
      // A stored post already joins its thread below.
      if (input && peer(author, /\(human\)$/.test(it.from || ''))) {
        const id = it.postId || `item:${t.n}:${i}`
        // A notice that opens a turn woke the agent; one that is steering landed mid-turn.
        if (it.postId) wakes.set(it.postId, { n: t.n, woke: it.kind === 'prompt' })
        if (it.postId) delivered.set(id, { delivery: it.delivered ? 'read' : 'delivered', pending: it.delivered ? undefined : receipt })
        if (!post) peerItems.set(id, { id, identified: !!it.postId, from: author, to: [address], ts: receipt?.ts ?? at, text: receipt?.text ?? it.text ?? '' })
        continue
      }
      if (input && author === voice) {
        if (it.postId) matched.add(it.postId)
        events.push({ kind: 'voice', key: it.postId ? `post:${it.postId}` : `item:${t.n}:${i}`, ts: post?.ts ?? receipt?.ts ?? at, text: post?.text ?? receipt?.text ?? it.text, post, delivery: it.delivered ? 'read' : 'delivered' })
        continue
      }
      if (input && it.postId) {
        matched.add(it.postId)
        events.push({ ...it, kind: 'message', itemKind: it.kind, author, human: post?.human ?? receipt?.human ?? /\(human\)$/.test(it.from || ''), ts: post?.ts ?? receipt?.ts ?? at, text: post?.text ?? receipt?.text ?? it.text, key: `post:${it.postId}`, post, pending: it.delivered ? undefined : receipt, delivery: it.delivered ? 'read' : 'delivered' })
        continue
      }
      const row = { ...it, itemKind: it.kind, author, human: /\(human\)$/.test(it.from || ''), ts: at, key: `item:${t.n}:${i}`, delivery: it.kind === 'steering' ? (it.delivered ? 'read' : 'delivered') : undefined, n: t.n }
      if (input || it.kind === 'reply') events.push({ ...row, kind: 'message' })
      else if (it.kind === 'tool') events.push({ ...row, kind: 'tool' })
      else events.push({ ...row, kind: isItemKind(it.kind) ? it.kind : 'note' })
    }
  }
  // A thread post carries its receipt for the thread panel: the transcript's
  // delivery first, then the queue's.
  const receiptOf = (p: ThreadPost): ThreadPost => {
    const q = receipts.get(p.id)
    const d = delivered.get(p.id) ?? (q && q.state !== 'delivered' ? { delivery: q.state === 'waiting' ? 'queued' : 'delivered', pending: q } : null)
    return d ? { ...p, ...d } : p
  }
  for (const p of session?.posts || []) {
    // Dyna's notice about changed standing instructions is a note, not a thread.
    if (isNote(p)) {
      events.push({ kind: 'instructions', key: `post:${p.id}`, ts: p.ts, text: p.text, note: noteData(p) })
      continue
    }
    if (voiceRequest(p, room.name)) {
      if (!matched.has(p.id)) events.push({ kind: 'voice', key: `post:${p.id}`, ts: p.ts, text: p.text, post: p, delivery: receipts.get(p.id)?.state === 'waiting' ? 'queued' : undefined })
      continue
    }
    const keys = threadKeys(p, address, room.name)
    for (const key of keys) threadFor(key).posts.push(receiptOf(p))
    events.push({ kind: 'post', key: `post:${p.id}`, ts: p.ts, post: p, threads: keys, suppressMessage: matched.has(p.id) || (receipts.has(p.id) && receipts.get(p.id)?.state !== 'delivered') })
  }
  for (const q of receipts.values()) {
    if (matched.has(q.id) || q.state === 'delivered') continue
    const post = posts.get(q.id)
    if (q.from === voice) {
      if (!post) events.push({ kind: 'voice', key: `post:${q.id}`, ts: q.ts, text: q.text, delivery: q.state === 'waiting' ? 'queued' : 'delivered' })
      continue
    }
    if (peer(q.from, q.human)) {
      if (!post && !peerItems.has(q.id)) peerItems.set(q.id, { id: q.id, identified: true, from: q.from, to: [address], ts: q.ts, text: q.text || '' })
      continue
    }
    events.push({ kind: 'message', key: `post:${q.id}`, ts: q.ts, author: q.from, human: !!(post?.human ?? q.human), text: q.text, post, pending: q, delivery: q.state === 'waiting' ? 'queued' : 'delivered' })
  }
  // A peer message that is also a post stays one message: the post wins.
  // Text decides only for transcript inputs without a post ID.
  const same = (a: string, b: string) => { a = a.trim(); b = b.trim(); return !!a && !!b && (a === b || a.includes(b) || b.includes(a)) }
  for (const p of peerItems.values()) {
    if ((threads.get(p.from)?.posts || []).some((q) => q.id === p.id || (!p.identified && q.from === p.from && same(q.text || '', p.text)))) continue
    threadFor(p.from).posts.push(receiptOf(p))
    events.push({ kind: 'post', key: `post:${p.id}`, ts: p.ts, post: p, threads: [p.from] })
  }
  for (const thread of threads.values()) thread.posts.sort((a, b) => time(a.ts) - time(b.ts))
  // On equal times a turn opens before its items and closes after them.
  const order: Record<string, number> = { turn: -1, turnEnd: 1 }
  events.sort((a, b) => time(a.ts) - time(b.ts) || (order[a.kind] || 0) - (order[b.kind] || 0))
  const rows: DmRow[] = []
  const shells = new Map<string, ShellRow>()
  const wokeBy = new Map<number, ShellJob>()
  const waiting = (p: ThreadPost) => receipts.get(p.id)?.state === 'waiting'
  for (const event of events) {
    if (event.kind === 'post') {
      const p = event.post
      if (!event.suppressMessage && (!event.threads.length || (p.from === address && (p.to || []).includes(room.name)))) {
        const ownerAttachments = (p.owner || p.from === room.name) && !!p.attachments?.length
        rows.push({ ...event, kind: 'message', author: p.from, text: p.text, human: !!p.human, ownerAttachments, attachmentOnly: false, post: p })
      }
      // A chip holds a run of posts where they arrived. A post joins a chip
      // directly above it; after any other row it starts a new chip.
      for (const key of event.threads) {
        // A job's end folds into the block of its call, when that is on the page.
        const block = shells.get(key.replace(/^shell:/, ''))
        if (key.startsWith('shell:') && block) {
          foldNotice(rows, block, p, wakes.get(p.id), wokeBy, address)
          continue
        }
        let chip: ThreadRow | undefined
        for (let i = rows.length - 1; !chip; i--) {
          const row = rows[i]
          if (row?.kind !== 'thread') break
          if (row.thread?.key === key) chip = row
        }
        if (!chip) {
          chip = { kind: 'thread', key: `thread:${key}:${p.id}`, thread: threads.get(key) ?? { key, posts: [] }, posts: [], ts: p.ts, queued: false }
          rows.push(chip)
        }
        chip.posts.push(p)
        if (key.startsWith('shell:')) chip.shell = key.slice('shell:'.length)
        chip.queued ||= waiting(p)
      }
    } else if (event.kind === 'tool' && foldShell(rows, shells, event, address)) {
      // A Dyna shell call is a terminal block, or a wait or stop on one.
    } else if (event.kind === 'tool') {
      const last = rows.at(-1)
      if (last?.kind === 'activity') last.items.push(event)
      else rows.push({ kind: 'activity', author: address, key: `activity:${event.key}`, ts: event.ts, items: [event] })
    } else if (event.kind === 'instructions' && event.note.slow) {
      // Slow-test issues that follow each other share one note row.
      const last = rows.at(-1)
      if (last?.kind === 'instructions' && last.notes?.[0]?.slow) last.notes.push(event.note)
      else rows.push({ ...event, notes: [event.note] })
    } else rows.push(event)
  }
  for (const row of rows) if (row.kind === 'turn' && wokeBy.has(row.turn.n)) row.wokeBy = wokeBy.get(row.turn.n)
  // Keep pending or clipped owner instructions visible. Only a returned
  // prompt with the same author and text can replace the post's text.
  const messages = rows.filter(isMessage)
  const prompts = messages.filter((row) => ['prompt', 'steering'].includes(row.itemKind ?? ''))
  for (const row of messages) {
    const text = row.text?.trim()
    if (!row.ownerAttachments || !text) continue
    const prompt = prompts.find((p) => p.author === row.author && !!p.human === !!row.human && p.text?.trim() === text && Math.abs(time(p.ts) - time(row.ts)) <= 2 * 60 * 1000)
    if (prompt) {
      row.attachmentOnly = true
      row.attachmentPrompt = prompt.key
      row.text = ''
    }
  }
  // A waking post can precede its prompt by milliseconds. Put adjacent owner
  // files below their matched prompt, even with a turn divider between them.
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i]
    if (row?.kind !== 'message' || !row.attachmentOnly) continue
    let next = i + 1
    while (rows[next]?.kind === 'turn') next++
    if (rows[next]?.key === row.attachmentPrompt) {
      const [files] = rows.splice(i, 1)
      if (files) rows.splice(next, 0, files)
      i = next
    }
  }
  // A turn reply that repeats this session's post to the owner from the same
  // turn folds under that post.
  const own = messages.filter((r) => r.post?.from === address && r.post.turn)
  for (const row of messages) if (row.itemKind === 'reply') row.folded = own.some((r) => r.post?.turn === row.n && sameText(r.text, row.text))
  // Only the newest read message shows its read label; pending states always
  // show. The delivery itself stays on every row.
  const reads = rows.filter((r) => r.delivery === 'read')
  for (const r of reads.slice(0, -1)) if (r.kind === 'message') r.quietReceipt = true
  return { address, threads: [...threads.values()], rows: groupMessages(foldQuietShells(rows)) }
}

// threadList orders the thread panel list newest first. Shell job
// completions fold into their own group. Every child gets a row, with its
// thread when one exists; children with recent posts come first.
export function threadList<M extends Pick<Member, 'name' | 'address'>>(threads: Thread[], children: M[] = []): { recent: Thread[]; shells: Thread[]; children: { member: M; thread: Thread | undefined }[] } {
  const last = (t?: Thread) => time(t?.posts.at(-1)?.ts)
  const sorted = [...threads].sort((a, b) => last(b) - last(a))
  const shell = (t: Thread) => t.key.startsWith('shell:')
  const childKeys = new Set(children.map((c) => c.address))
  return {
    recent: sorted.filter((t) => !shell(t) && !childKeys.has(t.key)),
    shells: sorted.filter(shell),
    children: children.map((member) => ({ member, thread: threads.find((t) => t.key === member.address) }))
      .sort((a, b) => last(b.thread) - last(a.thread) || a.member.name.localeCompare(b.member.name)),
  }
}

// sameText reports whether two texts are equal apart from whitespace. Any
// looser match can hide a correction in a word, link or path.
export function sameText(a?: string | null, b?: string | null): boolean {
  const norm = (text?: string | null) => String(text || '').replace(/\s+/g, ' ').trim()
  return !!norm(a) && norm(a) === norm(b)
}

// plain turns a post's markdown into one line of text for a thread chip.
export function plain(text?: string | null): string {
  return userText(text).text
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s*(?:#{1,6}\s+|>+\s*)/gm, '')
    .replace(/[`*_~]+/g, '')
    .replace(/\s+/g, ' ')
    .trim()
}

// A turn opens with a short divider, "Turn 1 · sol / gpt", and a finished
// turn closes with a footer: its status, then turnTail, "1m00s · 30 tokens".
export function turnHead(t: Pick<DmTurn, 'n' | 'profile' | 'model'>): string {
  return [`Turn ${t.n}`, t.profile && (t.model ? `${t.profile} / ${t.model}` : t.profile)].filter(Boolean).join(' · ')
}
export function turnTail(t: { started: string; ended?: string; usage?: Usage | null }): string {
  const output = t.usage?.outputTokens
  return [duration(t.started, t.ended), output && `${tokens((t.usage?.inputTokens ?? 0) + output)} tokens`].filter(Boolean).join(' · ')
}
