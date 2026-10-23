import { parsePRPost } from '../prs/pr.ts'
import type { ParsedPost, PREvent } from '../prs/pr.ts'
import type { PRNotice } from '../api/room.ts'
import type { CallRequest } from '../voice/callRequests.ts'
import { isHookEvent } from './events.gen.ts'
import type { EventPayload, HookEvent } from './events.gen.ts'

// The part of a stored post that feed rows read. A page of posts and the
// fixtures of a test both fit it.
export interface FeedInput {
  id: string
  ts: number
  from: string
  text?: string | null
  kind?: string
  notice?: PRNotice | null
  // payload is the typed JSON of a sealed system event.
  payload?: EventPayload
  to?: string[] | null
  human?: boolean
  owner?: boolean
  completion?: boolean
  replyTo?: string
  forward?: unknown
  attachments?: readonly unknown[]
}

// A day divider row.
export interface FeedDayRow {
  kind: 'day'
  key: string
  ts: number
}

// The "New" divider row above the first unread post.
export interface FeedNewRow {
  kind: 'new'
  key: string
}

// A call request row, at the time root asked.
export interface FeedCallRow {
  kind: 'call'
  key: string
  call: CallRequest
}

// A row of posts. A folded row keeps every post it shows, oldest first, and
// `post` is the newest one, which decides the row's state.
export interface FeedPostRow<P extends FeedInput = FeedInput> {
  kind: 'post'
  key: string
  post: P
  cont: boolean | null
  posts?: P[]
}

export type FeedRow<P extends FeedInput = FeedInput> = FeedDayRow | FeedNewRow | FeedCallRow | FeedPostRow<P>

const senderGap = 7 * 60 * 1000
const shellSender = /^shell:sh_\d{8}-\d{6}-[a-f0-9]{8}$/
const resultKinds: ReadonlySet<string> = new Set(['completed', 'failed', 'stopped'])

// Automatic `dyna shell` exit records stay out of channel feeds; the
// shell-jobs view lists those jobs. They still wake the job's parent.
export function isShellResult(post: FeedInput): boolean {
  return post.completion === true && resultKinds.has(post.kind ?? '') &&
    shellSender.test(post.from) && !post.owner && !post.human && !post.replyTo && !post.attachments?.length
}

// callsInWindow keeps the call requests that belong between the loaded
// posts: a window that stops short of older or newer posts shows no request
// from beyond its edge.
export function callsInWindow(calls: readonly CallRequest[], posts: readonly { ts: number }[], earlier = 0, newer = 0): CallRequest[] {
  const first = posts[0]?.ts ?? -Infinity
  const last = posts[posts.length - 1]?.ts ?? Infinity
  return calls.filter((c) => {
    const at = Date.parse(c.askedAt)
    return (!earlier || at >= first) && (!newer || at <= last)
  })
}

// rowPost is the newest post of a row, and undefined for a row that shows a
// divider or a call request.
export function rowPost<P extends FeedInput>(row: FeedRow<P> | undefined): P | undefined {
  return row?.kind === 'post' ? row.post : undefined
}

// rowPosts lists every post a row shows, oldest first.
export function rowPosts<P extends FeedInput>(row: FeedRow<P> | undefined): P[] {
  return row?.kind === 'post' ? row.posts ?? [row.post] : []
}

// hookEventOf returns the typed payload of a hook run post.
function hookEventOf(post: FeedInput): HookEvent | undefined {
  if (post.kind !== 'hook') return undefined
  return isHookEvent(post.payload) ? post.payload : undefined
}

// foldKey names what a row folds on: one card per PR, and one card per hook
// run, so a run's start folds into its result. An empty key never folds.
function foldKey(post: FeedInput): string {
  const hook = hookEventOf(post)
  if (hook) return `hook:${hook.runId}`
  if (post.kind === 'pr' && post.notice?.kind !== 'held-comment') {
    const url = parsePRPost(post.text, post.notice).url
    if (url) return `pr:${url}`
  }
  return ''
}

interface FeedItem<P extends FeedInput> {
  post?: P
  call?: CallRequest
  ts: number
}

// feedRows adds day and "new" dividers, and marks a post that continues the
// previous sender's run. Root and the human share the owner address, so the
// human flag is part of the sender. A call request is a row at the time root
// asked, between the posts.
export function feedRows<P extends FeedInput>(posts: readonly P[], unreadFrom = '', calls: readonly CallRequest[] = []): FeedRow<P>[] {
  const out: FeedRow<P>[] = []
  let prev: P | null = null
  let prevDay = ''
  const waiting = calls.map((call) => ({ call, ts: Date.parse(call.askedAt) })).sort((a, b) => a.ts - b.ts)
  const items: FeedItem<P>[] = []
  for (const post of posts) {
    while (waiting.length && (waiting[0]?.ts ?? Infinity) <= post.ts) {
      const next = waiting.shift()
      if (next) items.push(next)
    }
    items.push({ post, ts: post.ts })
  }
  items.push(...waiting)
  for (const { post, call, ts } of items) {
    const day = new Date(ts).toDateString()
    if (day !== prevDay) {
      out.push({ kind: 'day', key: 'd' + ts, ts })
      prevDay = day
      prev = null
    }
    if (call) {
      out.push({ kind: 'call', key: `call:${call.room}/${call.id}`, call })
      prev = null
      continue
    }
    if (!post) continue
    if (post.id === unreadFrom) {
      out.push({ kind: 'new', key: 'new' })
      prev = null
    }
    const gap = prev ? post.ts - prev.ts : Infinity
    const cont = prev && !post.completion && !prev.completion && post.kind !== 'pr' && prev.kind !== 'pr' &&
      !post.replyTo && prev.from === post.from && !!prev.human === !!post.human && gap < senderGap &&
      (prev.to || []).join() === (post.to || []).join()
    const last = out[out.length - 1]
    const key = foldKey(post)
    // A PR row and a hook row can fold, so both keep their posts from the start.
    const foldable = !!key || post.kind === 'pr'
    const plain = (p: FeedInput) => !p.replyTo && !p.forward && !p.attachments?.length
    if (key && last?.kind === 'post' && last.posts && plain(post) && plain(last.post) && foldKey(last.post) === key) {
      last.posts.push(post)
      last.post = post
    } else {
      out.push({ kind: 'post', key: post.id, post, cont, ...(foldable ? { posts: [post] } : {}) })
    }
    prev = post
  }
  return out
}

// Use the same parser as the PR screen. Noise counts belong in expanded detail.
export function feedPRDetail(post: { text?: string | null; notice?: PRNotice | null }): ParsedPost {
  const pr = parsePRPost(post.text, post.notice)
  return {
    ...pr, events: pr.events.filter((event) => event.kind !== 'noise'),
    body: pr.body.replace(/(?<![\w/])([a-f0-9]{40})(?![\w/])/gi, (sha) => sha.slice(0, 7)),
  }
}

export function feedPRRepos(posts: readonly FeedInput[]): Set<string> {
  return new Set(posts.filter((post) => post.kind === 'pr').map((post) => parsePRPost(post.text, post.notice).repo).filter(Boolean))
}

// A generic watcher update must not erase a known review of the same head.
// CI can change in an update, so only review state carries forward.
export function feedPREvents(details: readonly ParsedPost[]): PREvent[] {
  const last = details[details.length - 1]
  if (!last) return []
  if (!last.head || last.events.some((event) => event.kind !== 'update')) return last.events
  for (let i = details.length - 2; i >= 0; i--) {
    const detail = details[i]
    if (!detail || detail.head !== last.head) break
    const review = detail.events.filter((event) => ['approved', 'changes', 'review'].includes(event.kind))
    if (review.length) return review
  }
  return last.events
}
