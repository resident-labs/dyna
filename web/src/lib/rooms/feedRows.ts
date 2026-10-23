import { parsePRPost } from '../prs/pr.ts'
import type { EventNotice } from '../api/room.ts'
import type { CallRequest } from '../voice/callRequests.ts'
import type { EventPayload, HookEvent, HookState } from './events.gen.ts'
import { hookEvent, issueEvent, mergeEvent, prEvent } from './eventPayloads.ts'
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
}

// HOOK_PASS_WINDOW is how long after one pass of a hook the next pass still
// joins the same row. A dozen routine passes a minute apart read as one line,
// while passes an hour apart stay apart.
export const HOOK_PASS_WINDOW = 60 * 60 * 1000

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

// roomPosts keeps the posts of the room's own conversation. A session-audience
// event belongs to the one session it names and lives in that session's DM;
// the room lists it only when the owner is a recipient. The decision reads the
// typed audience, never the addresses it was posted to.
//
// TODO: the conversation series replaces this with the stored home
// (Message.Conversation and post_homes).
export function roomPosts<P extends FeedInput>(posts: readonly P[], owner: string): P[] {
  return posts.filter((post) => {
    const spec = eventSpecs.find((entry) => entry.family === post.kind && entry.type === post.notice?.kind)
    return spec?.audience !== 'session' || (post.to ?? []).includes(owner)
  })
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
  const pr = prEvent(post)
  if (pr) return pr.type === 'held-comment' ? '' : `pr:${pr.changeId || pr.url || ''}`
  // One card per issue: filed, changed and closed fold together. A filing has
  // no issue ID yet, so it folds on its own post ID, which is the issue's ID.
  const issue = issueEvent(post)
  if (issue) return `issue:${issue.issueId || post.id}`
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

// hookFold reads the passes a row of a hook holds: how many, and when the last
// one ended. The card draws these typed facts, never the post text.
function hookFold(posts: readonly FeedInput[]): HookFold | undefined {
  let count = 0
  let lastTs = 0
  for (const post of posts) {
    if (hookEvent(post)?.state !== 'passed') continue
    count++
    lastTs = Math.max(lastTs, post.ts)
  }
  return count ? { count, lastTs } : undefined
}

// hiddenHookStarts lists the starts of hook runs that show nowhere. A start
// shows only while its run is the newest thing of its hook: the engine posts a
// result for every start, so a start whose run ended in a failure or an error
// is already told by that failure's own card, and a start with no result while
// a later run of the same hook posted belongs to a run that ended unrecorded.
// Either way no stale "running" card is left behind.
function hiddenHookStarts(posts: readonly FeedInput[]): Set<string> {
  const resultState = new Map<string, HookState>()
  const lastOfHook = new Map<string, number>()
  posts.forEach((post, i) => {
    const hook = hookEvent(post)
    if (!hook) return
    if (hook.state !== 'running') resultState.set(hook.runId, hook.state)
    lastOfHook.set(hookIdentity(hook), i)
  })
  const hidden = new Set<string>()
  posts.forEach((post, i) => {
    const hook = hookEvent(post)
    if (hook?.state !== 'running') return
    const result = resultState.get(hook.runId)
    if (result !== undefined) {
      if (result !== 'passed') hidden.add(post.id)
      return
    }
    if ((lastOfHook.get(hookIdentity(hook)) ?? 0) > i) hidden.add(post.id)
  })
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
    if (hidden.has(post.id)) continue
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
