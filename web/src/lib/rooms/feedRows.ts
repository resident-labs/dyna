import { parsePRPost } from '../prs/pr.ts'
import type { EventNotice } from '../api/room.ts'
import type { CallRequest } from '../voice/callRequests.ts'
import type { EventPayload } from './events.gen.ts'
import { checkinEvent, hookEvent, issueEvent, mergeEvent, prEvent, sessionEvent, shellEvent } from './eventPayloads.ts'
import { eventSpecs } from './events.gen.ts'

// The part of a stored post that feed rows read. A page of posts and the
// fixtures of a test both fit it.
export interface FeedInput {
  id: string
  ts: number
  from: string
  text?: string | null
  kind?: string
  notice?: EventNotice | null
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

// Automatic `dyna shell` exit records stay out of feed views. A log link
// opens the owning session's Shell tab. They still wake the job's parent. A typed
// shell result is its own event; an older one names its job in the sender.
//
// TODO: legacy/shim remove later — the sender and kind form reads a shell
// result stored before the typed shell events. Remove when that history ages
// out.
export function isShellResult(post: FeedInput): boolean {
  if (shellEvent(post)) return !post.owner && !post.human && !post.replyTo && !post.attachments?.length
  return post.completion === true && resultKinds.has(post.kind ?? '') &&
    shellSender.test(post.from) && !post.owner && !post.human && !post.replyTo && !post.attachments?.length
}

// shellJobID names the shell job a result row opens. The ID stays opaque: it
// is the job's own key, never text a reader sees.
export function shellJobID(post: FeedInput): string {
  const typed = shellEvent(post)
  if (typed) return typed.shellId
  return post.from.slice('shell:'.length)
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

// foldKey names adjacent issue rows that can fold.
// An empty key never folds.
function foldKey(post: FeedInput): string {
  // One card per issue: filed, changed and closed fold together. A filing has
  // no issue ID yet, so it folds on its own post ID, which is the issue's ID.
  const issue = issueEvent(post)
  // A note carries its own evidence, so each note keeps its own card.
  if (issue) return issue.type === 'noted' ? '' : `issue:${issue.issueId || post.id}`
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
  const hidden = new Set<string>()
  const openThreads = new Map<string, P[]>()
  const threadRows = new Map<string, P[]>()
  const closeThread = (group: P[]) => {
    const newest = group[group.length - 1]
    if (!newest) return
    threadRows.set(newest.id, group)
    for (const post of group.slice(0, -1)) hidden.add(post.id)
  }
  for (const post of posts) {
    // TODO: legacy/shim remove after the upgrade floor excludes posts saved
    // before these events wrote their thread key.
    const legacyMerge = post.notice?.thread ? undefined : mergeEvent(post)
    const legacySession = post.notice?.thread ? undefined : sessionEvent(post)
    const legacyHook = post.notice?.thread ? undefined : hookEvent(post)
    const checkin = checkinEvent(post)
    const checkinThread = checkin?.attention === 'none' && !post.replyTo && !post.forward && !post.attachments?.length ? `checkin:${checkin.parent}` : ''
    const thread = checkinThread || post.notice?.thread || (legacyMerge?.trialId ? `merge:${legacyMerge.trialId}` : legacySession?.type === 'instructions' && legacySession.session ? `instructions:${legacySession.session}` : legacyHook?.runId ? `hook:${legacyHook.runId}` : '')
    if (!thread) continue
    const group = openThreads.get(thread) ?? []
    group.push(post)
    openThreads.set(thread, group)
    const spec = eventSpecs.find((entry) => entry.family === post.kind && entry.type === post.notice?.kind)
    if (spec?.threadEnd) {
      closeThread(group)
      openThreads.delete(thread)
    }
  }
  for (const group of openThreads.values()) closeThread(group)
  const markerGroup = [...threadRows.values()].find((group) => group.some((post) => post.id === unreadFrom))
  const unreadIndex = posts.findIndex((post) => post.id === unreadFrom)
  const marker = markerGroup && unreadIndex >= 0
    ? posts.slice(unreadIndex).find((post) => !hidden.has(post.id))?.id ?? unreadFrom
    : unreadFrom
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
    // Move a folded thread marker to the first visible unread row, so an
    // unrelated unread post never appears above the divider.
    if (post.id === marker) {
      out.push({ kind: 'new', key: 'new' })
      prev = null
    }
    if (hidden.has(post.id)) continue
    const gap = prev ? post.ts - prev.ts : Infinity
    const cont = prev && !post.completion && !prev.completion && post.kind !== 'pr' && prev.kind !== 'pr' &&
      !post.replyTo && prev.from === post.from && !!prev.human === !!post.human && gap < senderGap &&
      (prev.to || []).join() === (post.to || []).join()
    const last = out[out.length - 1]
    const threadPosts = threadRows.get(post.id)
    if (threadPosts) {
      const hook = hookEvent(post)
      // Other successful hook cards may be between runs in a burst. A live
      // or failed run, another post, or a gap over an hour breaks the fold.
      let target: FeedPostRow<P> | undefined
      if (hook?.state === 'passed') {
        for (let i = out.length - 1; i >= 0; i--) {
          const row = out[i]
          const previous = row?.kind === 'post' ? hookEvent(row.post) : undefined
          if (row?.kind !== 'post' || previous?.state !== 'passed') break
          if (previous.repo === hook.repo && previous.hook === hook.hook) {
            if (post.ts - row.post.ts <= 60 * 60 * 1000) target = row
            break
          }
        }
      }
      if (target) {
        target.posts = [...rowPosts(target), ...threadPosts]
        target.post = post
      } else {
        out.push({ kind: 'post', key: post.id, post, cont, posts: threadPosts })
      }
      prev = post
      continue
    }
    const key = foldKey(post)
    // A foldable row keeps its posts from the start.
    const foldable = !!key || post.kind === 'pr'
    const plain = (p: FeedInput) => !p.replyTo && !p.forward && !p.attachments?.length
    const open = last?.kind === 'post' ? last : undefined
    const openPosts = open?.posts
    const prevPost = open?.post
    // A pass of a hook joins its hook's row only within the window, so passes
    // far apart stay separate rows.
    const sameRow = !!key && !!openPosts && !!prevPost && plain(post) && plain(prevPost) &&
      foldKey(prevPost) === key
    if (sameRow && open && openPosts && prevPost) {
      openPosts.push(post)
      open.post = post
    } else {
      out.push({ kind: 'post', key: post.id, post, cont,
        ...(foldable ? { posts: [post] } : {}) })
    }
    prev = post
  }
  return out
}

// feedPRRepos lists the repositories of the PR rows on the page, so a row
// shows its repository only when the page holds more than one.
export function feedPRRepos(posts: readonly FeedInput[]): Set<string> {
  const out = new Set<string>()
  for (const post of posts) {
    if (post.kind !== 'pr') continue
    const repo = prEvent(post)?.repo || parsePRPost(post.text, post.notice).repo
    if (repo) out.add(repo)
  }
  return out
}
