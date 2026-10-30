import { parsePRPost } from '../prs/pr.ts'
import type { EventNotice } from '../api/room.ts'
import type { CallRequest } from '../voice/callRequests.ts'
import type { EventPayload, HookEvent } from './events.gen.ts'
import { hookEvent, issueEvent, mergeEvent, prEvent, reviewEvent, shellEvent } from './eventPayloads.ts'
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
  // hook is the fold of a row of hook passes: how many passes the row holds
  // and when the last one ended. A row of another kind has none.
  hook?: HookFold
}

// HookFold is what a folded row of hook passes shows.
export interface HookFold {
  count: number
  lastTs: number
  // active is a run of the same hook that has started and not ended: its start
  // is in the row, and no result of that run is. Two runs of one hook can be
  // live together, so the row takes the newest active run, whose log is the
  // one the card can open.
  active?: { runId: string; ts: number }
}

// HOOK_PASS_WINDOW is how long after one pass of a hook the next pass still
// joins the same row. A dozen routine passes a minute apart read as one line,
// while passes an hour apart stay apart.
export const HOOK_PASS_WINDOW = 60 * 60 * 1000

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

// hookIdentity names one hook of one repo: the subject a pass folds by.
function hookIdentity(hook: HookEvent): string {
  return `${hook.repo}\u0000${hook.hook}`
}

// isOpenHook reports whether a hook post may sit inside a fold: a pass, or the
// start of a run that has not ended yet. A failed or erroring run is its own
// card and ends the fold, so its next pass starts a new row.
function isOpenHook(hook: HookEvent): boolean {
  return hook.state === 'passed' || hook.state === 'running'
}

// hookRowKey names the row a hook post joins. Every pass of one hook folds
// into one row, and the start of a run joins that row too, so a running run
// shows as the running state of its hook. A failed or erroring run is its own
// card: no failure ever hides inside a fold.
function hookRowKey(hook: HookEvent): string {
  return isOpenHook(hook) ? `hook:${hookIdentity(hook)}` : `hook-run:${hook.runId}`
}

// rowPosts lists every post a row shows, oldest first.
export function rowPosts<P extends FeedInput>(row: FeedRow<P> | undefined): P[] {
  return row?.kind === 'post' ? row.posts ?? [row.post] : []
}

// foldKey names a row that folds on one subject: one card per PR, one row of
// passes per hook, one card per merge trial, so a trial's progress and its end
// stay one card, and one card per issue, so a filing, a change and a close
// fold together. An empty key never folds.
function foldKey(post: FeedInput): string {
  const hook = hookEvent(post)
  if (hook) return hookRowKey(hook)
  const merge = mergeEvent(post)
  if (merge) return `merge:${merge.trialId}`
  // One result card per PR head: a new head starts a new card.
  const review = reviewEvent(post)
  if (review) return `review:${review.changeId}:${review.head}`
  const pr = prEvent(post)
  if (pr) return pr.type === 'held-comment' ? '' : `pr:${pr.changeId || pr.url || ''}`
  // One card per issue: filed, changed and closed fold together. A filing has
  // no issue ID yet, so it folds on its own post ID, which is the issue's ID.
  const issue = issueEvent(post)
  // A note carries its own evidence, so each note keeps its own card.
  if (issue) return issue.type === 'noted' ? '' : `issue:${issue.issueId || post.id}`
  // TODO: legacy/shim remove later — a PR post stored before the typed events
  // names its change in text only; remove when that history ages out.
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

// hookFold reads the passes a row of a hook holds: how many, when the last one
// ended, and the run that is still going. The card draws these typed facts,
// never the post text. Only a start of an open run is ever in a row (see
// hiddenHookStarts), so a start in the row means its run has not ended.
function hookFold(posts: readonly FeedInput[]): HookFold | undefined {
  let count = 0
  let lastTs = 0
  let active: HookFold['active']
  for (const post of posts) {
    const hook = hookEvent(post)
    if (hook?.state === 'passed') {
      count++
      lastTs = Math.max(lastTs, post.ts)
    }
    // Two runs of one hook can be live together, so the newest start names the
    // run whose log the card opens.
    if (hook?.state === 'running') active = { runId: hook.runId, ts: post.ts }
  }
  if (!count && !active) return undefined
  return { count, lastTs, ...(active ? { active } : {}) }
}

// hiddenHookStarts lists the starts of hook runs that show nowhere. A start
// shows only while its run is open: the engine posts a result for every start
// that reaches one, and that result is the card's state and its log. So a
// start whose run has a result anywhere on the page shows nowhere, also when
// the result lands in another row after a message. A start with no result of
// its own stays visible, because runs can overlap: a scheduled run and a
// manual run of one hook can be live together, and another run's result says
// nothing about this one.
function hiddenHookStarts(posts: readonly FeedInput[]): Set<string> {
  const ended = new Set<string>()
  for (const post of posts) {
    const hook = hookEvent(post)
    if (hook && hook.state !== 'running') ended.add(hook.runId)
  }
  const hidden = new Set<string>()
  for (const post of posts) {
    const hook = hookEvent(post)
    if (hook?.state === 'running' && ended.has(hook.runId)) hidden.add(post.id)
  }
  return hidden
}

// openHookRow returns the row a hook post may still update when another hook's
// card sits between, as it does when two hooks fire in a burst: the walk back
// covers hook rows only, so a message, a day divider or any other row ends it
// and gives the next pass a row of its own. The folded row keeps its place in
// the feed and shows the newest pass's state and time, so the card updates in
// place instead of repeating down the page.
function openHookRow<P extends FeedInput>(out: FeedRow<P>[], key: string, ts: number): FeedPostRow<P> | undefined {
  for (let i = out.length - 1; i >= 0; i--) {
    const row = out[i]
    const hook = row?.kind === 'post' ? hookEvent(row.post) : undefined
    if (row?.kind !== 'post' || !hook || !isOpenHook(hook)) return undefined
    if (foldKey(row.post) === key) return ts - row.post.ts <= HOOK_PASS_WINDOW ? row : undefined
  }
  return undefined
}

// feedRows adds day and "new" dividers, and marks a post that continues the
// previous sender's run. Root and the human share the owner address, so the
// human flag is part of the sender. A call request is a row at the time root
// asked, between the posts.
export function feedRows<P extends FeedInput>(posts: readonly P[], unreadFrom = '', calls: readonly CallRequest[] = []): FeedRow<P>[] {
  const out: FeedRow<P>[] = []
  const hidden = hiddenHookStarts(posts)
  const openThreads = new Map<string, P[]>()
  const threadRows = new Map<string, P[]>()
  const closeThread = (group: P[]) => {
    const newest = group[group.length - 1]
    if (!newest) return
    threadRows.set(newest.id, group)
    for (const post of group.slice(0, -1)) hidden.add(post.id)
  }
  for (const post of posts) {
    const thread = post.notice?.thread
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
      out.push({ kind: 'post', key: post.id, post, cont, posts: threadPosts })
      prev = post
      continue
    }
    const key = foldKey(post)
    // A PR row, a hook row and a merge trial row can fold, so each keeps its
    // posts from the start.
    const foldable = !!key || post.kind === 'pr'
    const plain = (p: FeedInput) => !p.replyTo && !p.forward && !p.attachments?.length
    const hook = hookEvent(post)
    const open = last?.kind === 'post' ? last : undefined
    const openPosts = open?.posts
    const prevPost = open?.post
    // A pass of a hook joins its hook's row only within the window, so passes
    // far apart stay separate rows.
    const sameRow = !!key && !!openPosts && !!prevPost && plain(post) && plain(prevPost) &&
      foldKey(prevPost) === key && (!hook || post.ts - prevPost.ts <= HOOK_PASS_WINDOW)
    const skipped = sameRow ? undefined : hook && plain(post) ? openHookRow(out, key, post.ts) : undefined
    if (sameRow && open && openPosts && prevPost) {
      openPosts.push(post)
      open.post = post
      if (hook) open.hook = hookFold(openPosts)
    } else if (skipped?.posts) {
      skipped.posts.push(post)
      skipped.post = post
      skipped.hook = hookFold(skipped.posts)
    } else {
      out.push({ kind: 'post', key: post.id, post, cont,
        ...(foldable ? { posts: [post] } : {}),
        ...(hook && foldable ? { hook: hookFold([post]) } : {}) })
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
