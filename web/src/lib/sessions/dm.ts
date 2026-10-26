import type { Member, Post } from '../api/room.ts'
import type { Item, QueueState, SessionReference, Turn, Usage } from '../api/session.ts'
import type { ShellJob } from '../api/terminal.ts'
import { isSessionReference } from '../api/guards.ts'
import { isNote, noteData } from '../ui/notices.ts'
import { shellEvent, systemEvent } from '../rooms/eventPayloads.ts'
import { userText } from './userText.ts'
import { duration, short, tokens } from '../ui/format.ts'

type Time = string | number

// A queued message as the DM reads it. The engine's QueueItem has all of
// these; ts is unix milliseconds there.
export interface Receipt {
  id: string
  from: string
  state: QueueState
  ts: Time
  text?: string
  human?: boolean
  cancellable?: boolean
  turn?: number
}

interface ReceiptProjection {
  include: boolean
  delivery?: 'queued' | 'delivered'
}

// Every QueueState needs a receipt projection. The helper's undefined return
// also keeps an unrecognized state from a server out of the DM.
const receiptProjections: Record<QueueState, ReceiptProjection> = {
  waiting: { include: true, delivery: 'queued' },
  delivering: { include: true, delivery: 'delivered' },
  delivered: { include: true },
  cancelled: { include: false },
  dismissed: { include: false },
}
const receiptProjection = (state: QueueState): ReceiptProjection | undefined => receiptProjections[state]
const receiptDelivery = (receipt: Receipt | undefined): ReceiptProjection['delivery'] => receipt ? receiptProjection(receipt.state)?.delivery : undefined

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

// The note a row carries, such as a changed instruction.
export interface DmNote {
  id?: string
  target?: string
  text?: string
  ts?: Time
  attachments?: Post['attachments']
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
// A delivered engine notice with a registered event draws the same card as the
// feed (system-events.md section 3.4.2), with the agent text in its details.
export interface SystemEventRow extends RowFields {
  kind: 'system-event'
  post: ThreadPost
}
export interface ThreadRow extends RowFields {
  kind: 'thread'
  thread: Thread
  posts: ThreadPost[]
  queued: boolean
}
// A turn has one list of steps. Shell reads and completion notices update the
// original step; a child session keeps its messages in the same step.
export interface ToolRow extends RowFields {
  kind: 'tool'
}
export interface ShellStep extends RowFields {
  kind: 'shell'
  jobId: string
  job?: ShellJob
  waits: number
  posts: ThreadPost[]
  finished?: { at: Time; turn?: number; woke: boolean }
}
export interface ChildStep extends RowFields {
  kind: 'child'
  member: TimelineChild
  thread: Thread
  posts: ThreadPost[]
}
export type TimelineStep = ToolRow | ShellStep | ChildStep
export interface TimelineRow extends RowFields {
  kind: 'timeline'
  turn?: DmTurn
  steps: TimelineStep[]
}
export type TimelineChild = Pick<Member, 'id' | 'name' | 'address' | 'created' | 'state' | 'status'> & Partial<Member>
export interface InstructionsRow extends RowFields {
  kind: 'instructions'
  note: DmNote
  notes?: DmNote[]
}
// A transcript item that shows as it is: a thought, an error, a note or a compaction divider.
export interface ItemRow extends RowFields {
  kind: 'thought' | 'error' | 'note' | 'compaction'
}
// A reference row is one post of another conversation: who wrote it, where it
// lives and its first line, with one action that opens it there.
export interface ReferenceRow extends RowFields {
  kind: 'reference'
  reference: SessionReference
}
// One row of the DM.
export type DmRow = TurnRow | TurnEndRow | MessageRow | VoiceRow | SystemEventRow | ThreadRow | TimelineRow | InstructionsRow | ItemRow | ReferenceRow
// A post waiting to become a row or a chip.
interface PostEvent extends RowFields {
  kind: 'post'
  post: ThreadPost
  threads: string[]
  suppressMessage?: boolean
}
type DmEvent = DmRow | TimelineStep | PostEvent

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
// postedTo is the "posted to" chip of a DM row. A post written in a DM is to
// that conversation's reader, so the chip says nothing there; any other post
// names the room's chat and the recipients it was addressed to. Rooms carry no
// '#' prefix, as the reference rows' "from Room chat" does.
export function postedTo(post: Pick<ThreadPost, 'to' | 'conversation'>, roomName: string): string {
  if ((post.conversation || '').startsWith('dm:')) return ''
  return `Room chat · ${(post.to || []).map((a) => '@' + short(a, roomName)).join(', ')}`
}
// referenceHome names the conversation a reference row comes from, for a reader
// without IDs: the room's chat, the voice call, or the DM the post was written
// in. members resolve that DM's session when the room knows it.
export function referenceHome(conversation: string, members: Pick<Member, 'id' | 'name' | 'address'>[]): string {
  if (conversation === 'voice') return 'Voice'
  if (!conversation.startsWith('dm:')) return 'Room chat'
  const id = conversation.slice('dm:'.length)
  const member = members.find((m) => m.id === id || m.address === id)
  return member ? `the DM with @${member.name}` : 'a direct message'
}
// threadKeys are the agents a post of the DM belongs to as threads. A post the
// owner wrote is the owner's own words, so it opens no thread.
export function threadKeys(post: Pick<ThreadPost, 'from' | 'to' | 'owner' | 'human'>, address: string, owner: string): string[] {
  if (post.owner || post.from === owner) return []
  return [...new Set(post.from === address ? (post.to || []).filter((a) => a !== owner && a !== address) : [post.from])].flatMap((key) => (key ? [key] : []))
}

// voiceRequest reports whether post is Voice's request on the owner's behalf:
// a post from the room's voice call that is not a line of the call itself.
export function voiceRequest(post: Pick<ThreadPost, 'from' | 'kind'>, roomName: string): boolean {
  return post.from === `${roomName}:voice-call` && post.kind !== 'voice'
}

// A shell completion can wake a later turn or arrive during the current turn.
interface Wake {
  n: number
  woke: boolean
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
    if (row.kind !== 'message' && !(['timeline', 'thought'].includes(row.kind) && row.author)) return row
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
  references?: SessionReference[] | null
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
export function dmView(session: DmSession | null | undefined, room: { name: string }, show: { reasoning?: boolean; compaction?: boolean } = {}, queue: Receipt[] = [], children: TimelineChild[] = []): DmView {
  const address = sessionAddress(session, room)
  const events: DmEvent[] = [], threads = new Map<string, Thread>(), peerItems = new Map<string, PeerItem>(), wakes = new Map<string, Wake>()
  const posts = new Map<string, ThreadPost>((session?.posts || []).map((p): [string, ThreadPost] => [p.id, p]))
  // A post of another conversation that this one received or wrote. It is one
  // row that opens the post where it lives.
  const references = new Map<string, SessionReference>((session?.references || []).filter(isSessionReference).map((r): [string, SessionReference] => [r.postId, r]))
  const receipts = new Map<string, Receipt>(queue.filter((q) => receiptProjection(q.state)?.include).map((q): [string, Receipt] => [q.id, q]))
  const matched = new Set<string>(), carried = new Set<string>(), delivered = new Map<string, { delivery: string; pending?: Receipt }>()
  const shownReferences = new Set<string>()
  const threadFor = (key: string): Thread => {
    let thread = threads.get(key)
    if (!thread) { thread = { key, posts: [] }; threads.set(key, thread) }
    return thread
  }
  // Only the owner and this session speak inline; every other agent is a thread.
  // Voice's requests are the owner's words, so they stay inline as voice rows.
  const voice = `${room.name}:voice-call`
  const peer = (from: string, human?: boolean) => !human && from !== room.name && from !== address && from !== voice
  // A reference row of another agent also joins that agent's thread, so the
  // thread panel keeps what the agent said to this session.
  const threadReference = (ref: SessionReference) => {
    if (!peer(ref.from)) return
    threadFor(ref.from).posts.push({ id: ref.postId, from: ref.from, to: [address], ts: ref.ts, text: ref.preview })
  }
  // pushReference draws one post of another conversation as its reference row,
  // once, whether a pin, a turn item or the row list names it first.
  const pushReference = (ref: SessionReference) => {
    if (shownReferences.has(ref.postId) || posts.has(ref.postId)) return
    shownReferences.add(ref.postId)
    matched.add(ref.postId)
    threadReference(ref)
    events.push({ kind: 'reference', key: `reference:${ref.postId}`, ts: ref.ts, author: ref.from, reference: ref })
  }
  for (const t of session?.turns || []) {
    events.push({ kind: 'turn', key: `turn:${t.n}`, ts: t.started, turn: t })
    if (done.includes(t.status ?? '') && t.ended) events.push({ kind: 'turnEnd', key: `turn-end:${t.n}`, ts: t.ended, turn: t })
    if (t.omitted) events.push({ kind: 'note', key: `omitted:${t.n}`, ts: t.started, text: 'Earlier events of this turn were left out.' })
    // Items arrive in the order they happened. Their times can run backward (a
    // folded thought keeps its latest time), and the sort below must not
    // reorder them, so no item is earlier than the one before it.
    let floor: Time = 0
    for (const [i, it] of (t.items || []).entries()) {
      // A wrap-up prompt is engine plumbing, not the owner's writing. The
      // typed turn divider names the check without showing that prompt.
      if (t.wrapUp && it.kind === 'prompt') continue
      if (it.kind === 'thought' && show.reasoning === false) continue
      if (it.kind === 'compaction' && show.compaction === false) continue
      const input = ['prompt', 'steering'].includes(it.kind)
      const author = input ? (it.from || room.name).replace(/\s*\(human\)$/, '') : address
      // The time an item shows: its own, or its turn's start when it has none.
      let at: Time = it.ts && time(it.ts) ? it.ts : t.started
      if (time(at) < time(floor)) at = floor
      floor = at
      // Match the stored post ID; transcript text can be framed or clipped.
      const receipt = it.postId ? receipts.get(it.postId) : undefined
      const post = it.postId ? posts.get(it.postId) : undefined
      // The post lives in another conversation, so the DM reads it as the
      // reference row: the item only says how and when it arrived.
      const reference = it.postId ? references.get(it.postId) : undefined
      if (input && reference) {
        pushReference(reference)
        continue
      }
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
        // A shell result stays with the step of the job it ended, so it never
        // becomes a card of its own. The item still says who it woke.
        if (post && shellEvent(post)) {
          wakes.set(it.postId, { n: t.n, woke: it.kind === 'prompt' })
          carried.add(it.postId)
          delivered.set(it.postId, { delivery: it.delivered ? 'read' : 'delivered', pending: it.delivered ? undefined : receipt })
          continue
        }
        if (post && systemEvent(post)) {
          // A notice that opened or steered this turn draws its card, the same
          // one the feed shows, instead of its raw text.
          matched.add(it.postId)
          events.push({ kind: 'system-event', key: `post:${it.postId}`, ts: post.ts ?? at, post, author, text: post.text })
          continue
        }
        // A stored post draws its own row below, from the post alone: its text,
        // its attachments, its forward and its references. The turn item says
        // only when and how the post reached this session.
        if (post) {
          carried.add(it.postId)
          delivered.set(it.postId, { delivery: it.delivered ? 'read' : 'delivered', pending: it.delivered ? undefined : receipt })
          continue
        }
        // The room no longer holds the post, or the queue has not posted it
        // yet. No post row exists, so the item stands in for it.
        matched.add(it.postId)
        events.push({ ...it, kind: 'message', itemKind: it.kind, author, human: receipt?.human ?? /\(human\)$/.test(it.from || ''), ts: receipt?.ts ?? at, text: receipt?.text ?? it.text, key: `post:${it.postId}`, post, pending: it.delivered ? undefined : receipt, delivery: it.delivered ? 'read' : 'delivered' })
        continue
      }
      const row = { ...it, itemKind: it.kind, author, human: /\(human\)$/.test(it.from || ''), ts: at, key: `item:${t.n}:${i}`, delivery: it.kind === 'steering' ? (it.delivered ? 'read' : 'delivered') : undefined, n: t.n }
      if (input || it.kind === 'reply') events.push({ ...row, kind: 'message' })
      else if (it.kind === 'tool') events.push({ ...row, kind: 'tool' })
      else events.push({ ...row, kind: isItemKind(it.kind) ? it.kind : 'note' })
    }
  }
  for (const child of children) {
    const at = time(child.created)
    const turn = (session?.turns || []).findLast((t) => time(t.started) <= at)
    // Do not attach a child from outside the loaded transcript to this page.
    if (!turn || (done.includes(turn.status ?? '') && turn.ended && at > time(turn.ended))) continue
    events.push({ kind: 'child', key: `child:${turn.n}:${child.id ?? child.address}`, ts: child.created, n: turn.n, member: child, thread: threadFor(child.address), posts: [] })
  }
  // A thread post carries its receipt for the thread panel: the transcript's
  // delivery first, then the queue's.
  const receiptOf = (p: ThreadPost): ThreadPost => {
    const q = receipts.get(p.id)
    const delivery = receiptDelivery(q)
    const d = delivered.get(p.id) ?? (delivery ? { delivery, pending: q } : null)
    return d ? { ...p, ...d } : p
  }
  for (const p of session?.posts || []) {
    // Dyna's notice about changed standing instructions is a note, not a thread.
    // TODO: legacy/shim remove later — a typed session note draws its card
    // instead; only posts stored before the typed events reach this row.
    if (isNote(p)) {
      events.push({ kind: 'instructions', key: `post:${p.id}`, ts: p.ts, text: p.text, note: noteData(p) })
      continue
    }
    if (voiceRequest(p, room.name)) {
      if (!matched.has(p.id)) events.push({ kind: 'voice', key: `post:${p.id}`, ts: p.ts, text: p.text, post: p, delivery: receiptDelivery(receipts.get(p.id)) === 'queued' ? 'queued' : undefined })
      continue
    }
    // An engine event is not a conversation with a peer: it draws its card, so
    // it opens no thread and names no member. A shell's result is the exception:
    // it joins the step of the job it ends, so the timeline keeps one step per
    // command with its output and who it woke.
    const engine = systemEvent(p)
    const shell = shellEvent(p)
    const keys = shell ? [`shell:${shell.shellId}`] : engine ? [] : threadKeys(p, address, room.name)
    for (const key of keys) threadFor(key).posts.push(receiptOf(p))
    // A carried post draws its own message row; the queue's receipt does not
    // replace it, because the row below takes the delivery from the turn item.
    events.push({ kind: 'post', key: `post:${p.id}`, ts: p.ts, post: p, threads: keys, suppressMessage: !!shell || matched.has(p.id) || (!carried.has(p.id) && receiptDelivery(receipts.get(p.id)) !== undefined) })
  }
  for (const q of receipts.values()) {
    const delivery = receiptDelivery(q)
    if (matched.has(q.id) || carried.has(q.id) || !delivery) continue
    const post = posts.get(q.id)
    if (post && systemEvent(post)) {
      // A queued engine event draws its card, not a bubble with the raw text
      // and not a thread with the engine as a peer.
      events.push({ kind: 'system-event', key: `post:${q.id}`, ts: post.ts ?? q.ts, post, author: post.from, text: post.text, delivery })
      continue
    }
    if (q.from === voice) {
      if (!post) events.push({ kind: 'voice', key: `post:${q.id}`, ts: q.ts, text: q.text, delivery })
      continue
    }
    if (peer(q.from, q.human)) {
      if (!post && !peerItems.has(q.id)) peerItems.set(q.id, { id: q.id, identified: true, from: q.from, to: [address], ts: q.ts, text: q.text || '' })
      continue
    }
    events.push({ kind: 'message', key: `post:${q.id}`, ts: q.ts, author: q.from, human: !!(post?.human ?? q.human), text: q.text, post, pending: q, delivery })
  }
  // A post of another conversation is one compact row, in the order it was
  // written, that jumps to the post there.
  for (const reference of references.values()) pushReference(reference)
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
  const shells = new Map<string, ShellStep>()
  // The open timeline of each turn. A row of another kind (a thought, a reply,
  // an error) closes it, so the next step starts a new one and the turn reads
  // in the order it happened. Only a run of steps shares a timeline.
  const timelines = new Map<number, TimelineRow>()
  const segments = new Map<number, TimelineRow[]>()
  const turns = session?.turns || []
  let current: DmTurn | undefined
  const timeline = (n: number | undefined, at?: Time): TimelineRow => {
    const id = n ?? current?.n ?? 0
    let row = timelines.get(id)
    if (!row || rows.at(-1) !== row) {
      const made = segments.get(id) ?? []
      row = { kind: 'timeline', author: address, key: made.length ? `timeline:${id}:${made.length}` : `timeline:${id}`, ts: at, turn: turns.find((t) => t.n === id), steps: [] }
      segments.set(id, [...made, row])
      timelines.set(id, row)
      rows.push(row)
    }
    return row
  }
  const shellNotice = (id: string, post: ThreadPost): void => {
    let step = shells.get(id)
    if (!step) {
      step = { kind: 'shell', key: `shell:${id}`, ts: post.ts, jobId: id, waits: 0, posts: [] }
      shells.set(id, step)
      timeline(undefined, post.ts).steps.push(step)
    }
    step.posts.push(post)
    const wake = wakes.get(post.id)
    step.finished = { at: post.ts ?? 0, turn: wake?.n, woke: !!wake?.woke }
  }
  const childStep = (child: TimelineChild, at?: Time, n?: number): ChildStep => {
    const id = n ?? current?.n ?? 0
    const found = (segments.get(id) ?? []).flatMap((r) => r.steps).find((s): s is ChildStep => s.kind === 'child' && s.member.address === child.address)
    if (found) return found
    const row = timeline(n, at)
    const step: ChildStep = { kind: 'child', key: `child:${row.turn?.n ?? 0}:${child.id ?? child.address}`, ts: at, member: child, thread: threadFor(child.address), posts: [] }
    row.steps.push(step)
    return step
  }
  const waiting = (p: ThreadPost) => receiptDelivery(receipts.get(p.id)) === 'queued'
  for (const event of events) {
    if (event.kind === 'turn') current = event.turn
    if (event.kind === 'post') {
      const p = event.post
      if (!event.suppressMessage && (!event.threads.length || (p.from === address && (p.to || []).includes(room.name)))) {
        if (systemEvent(p)) {
          // A registered event is a card, not a message. The agent text stays
          // on the card, behind its Details toggle.
          rows.push({ kind: 'system-event', key: `post:${p.id}`, ts: p.ts, post: p, author: p.from, text: p.text })
        } else {
          const ownerAttachments = (p.owner || p.from === room.name) && !!p.attachments?.length
          const receipt = carried.has(p.id) ? delivered.get(p.id) : undefined
          rows.push({ ...event, kind: 'message', author: p.from, text: p.text, human: !!p.human, ownerAttachments, attachmentOnly: false, post: p, delivery: receipt?.delivery, pending: receipt?.pending })
        }
      }
      for (const key of event.threads) {
        if (key.startsWith('shell:')) {
          shellNotice(key.slice('shell:'.length), p)
          continue
        }
        const child = children.find((c) => c.address === key)
        if (child) {
          childStep(child, p.ts).posts.push(p)
          continue
        }
        let chip: ThreadRow | undefined
        for (let i = rows.length - 1; !chip; i--) {
          const row = rows[i]
          if (row?.kind !== 'thread') break
          if (row.thread.key === key) chip = row
        }
        if (!chip) {
          chip = { kind: 'thread', key: `thread:${key}:${p.id}`, thread: threadFor(key), posts: [], ts: p.ts, queued: false }
          rows.push(chip)
        }
        chip.posts.push(p)
        chip.queued ||= waiting(p)
      }
    } else if (event.kind === 'tool') {
      if (event.jobOp === 'run' && event.job) {
        // A completion can precede a clipped run item. Reuse its step.
        const prior = shells.get(event.job.id)
        if (prior) prior.job = event.job
        else {
          const step: ShellStep = { kind: 'shell', key: `shell:${event.job.id}`, ts: event.ts, job: event.job, jobId: event.job.id, waits: 0, posts: [] }
          shells.set(event.job.id, step)
          timeline(event.n, event.ts).steps.push(step)
        }
      } else {
        const step = event.jobId ? shells.get(event.jobId) : undefined
        if (step) step.waits++
        else timeline(event.n, event.ts).steps.push(event)
      }
    } else if (event.kind === 'child') {
      childStep(event.member, event.ts, event.n)
    } else if (event.kind !== 'shell') rows.push(event)
  }
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
  // Shell job completions are timeline steps, not threads.
  return { address, threads: [...threads.values()].filter((t) => !t.key.startsWith('shell:')), rows: groupMessages(rows) }
}

// A row of the thread panel: an agent and the thread between it and this session.
export interface PanelRow<M> {
  member: M
  thread: Thread | undefined
}

// The thread panel groups its rows by relationship: the session's open direct
// children, its parent, its open siblings, then every other thread. A closed
// child leaves the panel, thread and all. A grandchild is not a child, so its
// thread counts as another thread.
export interface ThreadPanel<M> {
  children: PanelRow<M>[]
  parent: PanelRow<M> | null
  siblings: PanelRow<M>[]
  others: Thread[]
}

export function threadPanel<M extends Pick<Member, 'name' | 'address' | 'parent' | 'closed'> & { id?: string }>(threads: Thread[], members: M[], session: Partial<Pick<Member, 'id' | 'parent'>> = {}): ThreadPanel<M> {
  const last = (t?: Thread) => time(t?.posts.at(-1)?.ts)
  const shown = new Set<string>()
  const row = (member: M): PanelRow<M> => {
    shown.add(member.address)
    return { member, thread: threads.find((t) => t.key === member.address) }
  }
  const rows = (list: M[]) => list.map(row).sort((a, b) => last(b.thread) - last(a.thread) || a.member.name.localeCompare(b.member.name))
  const open = members.filter((x) => !x.closed)
  const children = rows(session.id ? open.filter((x) => x.parent === session.id) : [])
  const parent = (session.parent ? open.find((x) => x.id === session.parent) : undefined) ?? null
  const siblings = session.parent ? rows(open.filter((x) => !!x.id && x.parent === session.parent && x.id !== session.id)) : []
  // A closed child is gone from the panel, so its thread is not another thread either.
  const gone = new Set(members.filter((x) => x.closed && x.parent === session.id).map((x) => x.address))
  return {
    children,
    parent: parent ? row(parent) : null,
    siblings,
    others: threads.filter((t) => !shown.has(t.key) && !gone.has(t.key)).sort((a, b) => last(b) - last(a)),
  }
}

// threadPosts is the number of posts a member row links to. A row with no
// thread yet, or a thread with no posts, has zero.
export const threadPosts = <M>(row: PanelRow<M>): number => row.thread?.posts.length ?? 0

// threadsListed counts the threads the panel shows: a member row that links to
// posts, plus every other thread. A closed child leaves the panel and its
// thread with it, so its thread is not counted.
export function threadsListed<M>(panel: ThreadPanel<M>): number {
  const rows: PanelRow<M>[] = [...panel.children, ...(panel.parent ? [panel.parent] : []), ...panel.siblings]
  return rows.filter((row) => threadPosts(row) > 0).length + panel.others.length
}

// listedThreadCount is the one count used by the thread panel and the header
// badge, so neither can count a closed child's or an empty thread by mistake.
export function listedThreadCount<M extends Pick<Member, 'name' | 'address' | 'parent' | 'closed'> & { id?: string }>(threads: Thread[], members: M[], session: Partial<Pick<Member, 'id' | 'parent'>> = {}): number {
  return threadsListed(threadPanel(threads, members, session))
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

// modelName is the readable name of a model slug: "deepseek/deepseek-flash"
// reads as "deepseek-flash". A turn shows one name, never the raw pair.
export function modelName(model?: string | null): string {
  return (model || '').split('/').filter(Boolean).pop() ?? ''
}

// A turn opens with a short divider, "Turn 1 · sol", and a finished turn
// closes with a footer: its status, then turnTail, "1m00s · 30 tokens".
// The profile is the display name, so the raw model slug only stands in when
// a turn has no profile.
export function turnHead(t: Pick<DmTurn, 'n' | 'profile' | 'model' | 'wrapUp'>): string {
  return [t.wrapUp ? 'Wrap-up check' : `Turn ${t.n}`, t.profile || modelName(t.model)].filter(Boolean).join(' · ')
}
export function turnTail(t: { started: string; ended?: string; usage?: Usage | null }): string {
  const output = t.usage?.outputTokens
  return [duration(t.started, t.ended), output && `${tokens((t.usage?.inputTokens ?? 0) + output)} tokens`].filter(Boolean).join(' · ')
}
