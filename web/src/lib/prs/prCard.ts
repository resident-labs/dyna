// The facts a PR card draws, read from the typed payload the watcher posts.
// A post the watcher stored before the typed events has no payload; its text
// reader is pr.ts, kept only for that stored history.
import { parsePRPost } from './pr.ts'
import type { ParsedPost } from './pr.ts'
import { prEvent } from '../rooms/eventPayloads.ts'
import type { EventPayload, PREvent as TypedPREvent, PRType } from '../rooms/events.gen.ts'

// The part of a post a PR card reads.
export interface PRPostInput {
  id: string
  ts?: string | number
  kind?: string
  text?: string | null
  notice?: { kind: string } | null
  payload?: EventPayload
}

// Chip is one label a card shows, such as "CI failed 2/3".
export interface Chip {
  label: string
  tone: string
}

// EventNoticeView is one folded post of a PR card: its body and its log tail.
export interface EventNoticeView {
  id: string
  ts?: string | number
  body: string
  log: string
  dropped: number
  held: boolean
  changeId: string
  commentId: string
}

// PRView is what a PR card draws.
export interface PRView {
  number: number
  repo: string
  title: string
  head: string
  url: string
  changeId: string
  chips: Chip[]
  notices: EventNoticeView[]
  legacy: boolean
}

// chipOf names one event in the strength order a row shows it.
export function chipOf(ev: TypedPREvent): Chip {
  const attempt = ev.ciAttempt && ev.ciMax ? ` ${Math.min(ev.ciAttempt, ev.ciMax)}/${ev.ciMax}` : ''
  switch (ev.type) {
    case 'ci-failed': return { label: `CI failed${attempt}`, tone: 'coral' }
    case 'ci-paused': return { label: 'CI wakes paused after 3 failures', tone: 'gold' }
    case 'ci-passed': return { label: 'CI passed', tone: 'sage' }
    case 'changes': return ev.verdict === 'question' ? { label: 'Question for the author', tone: 'gold' } : { label: 'Changes requested', tone: 'gold' }
    case 'approved': return { label: ev.verdict === 'approve-with-nits' ? 'Approved with nits' : 'Approved by the reviewer', tone: 'sage' }
    case 'ready': return { label: 'Ready to land', tone: 'sage' }
    case 'opened': return { label: 'Opened', tone: 'quiet' }
    case 'returned': return { label: 'Returned to you', tone: 'gold' }
    case 'merged': return { label: 'Merged', tone: 'sage' }
    case 'closed': return { label: 'Closed', tone: 'faint' }
    case 'comment': return { label: ev.commenter ? `Comment from ${ev.commenter}` : 'New comment', tone: 'quiet' }
    case 'held-comment': return { label: 'Comment held', tone: 'gold' }
    case 'review-assigned': return { label: 'Review assigned', tone: 'quiet' }
    default: return { label: 'Update', tone: 'quiet' }
  }
}

// reviewChips are the events a generic update must not erase on the same head.
const REVIEW_TYPES: ReadonlySet<PRType> = new Set(['approved', 'changes', 'review-assigned'])

// prView folds a PR row's posts into one card. The newest post decides the
// facts and the strongest chip; a plain update keeps the review chip of the
// same head, as the feed did before the typed events.
export function prView(posts: readonly PRPostInput[]): PRView {
  const typed = posts.map((post) => prEvent(post))
  const lastTyped = typed.filter((ev): ev is TypedPREvent => !!ev).at(-1)
  const last = posts[posts.length - 1]
  const details = posts.map((post) => parsePRPost(post.text, post.notice))
  const legacyFacts = details[details.length - 1] ?? emptyDetail()
  const legacy = !lastTyped

  let chips: Chip[] = []
  if (lastTyped) {
    const sameHead = typed.filter((ev): ev is TypedPREvent => !!ev && ev.head === lastTyped.head)
    const kept = lastTyped.type === 'update' ? sameHead.findLast((ev) => REVIEW_TYPES.has(ev.type)) : undefined
    chips = [chipOf(kept ?? lastTyped)]
  } else {
    chips = details.length ? legacyChips(details) : []
  }

  return {
    number: lastTyped?.number ?? legacyFacts.number,
    repo: lastTyped?.repo ?? legacyFacts.repo,
    title: lastTyped?.title ?? '',
    head: lastTyped?.head ?? legacyFacts.head,
    url: lastTyped?.url ?? legacyFacts.url,
    changeId: lastTyped?.changeId ?? '',
    chips,
    notices: posts.map((post, i): EventNoticeView => {
      const ev = typed[i]
      const detail = details[i] ?? emptyDetail()
      if (ev) {
        return {
          id: post.id,
          ts: post.ts,
          body: ev.detail ?? '',
          log: ev.logTail ?? '',
          dropped: ev.dropped ?? 0,
          held: ev.type === 'held-comment',
          changeId: ev.changeId,
          commentId: ev.commentId ?? '',
        }
      }
      return { id: post.id, ts: post.ts, body: shortenShas(detail.body), log: detail.log, dropped: detail.dropped, held: post.notice?.kind === 'held-comment', changeId: '', commentId: '' }
    }),
    legacy,
  }
}

// shortenShas shortens a standalone commit SHA in the body of a legacy post,
// which the feed did before the typed events.
function shortenShas(body: string): string {
  return body.replace(/(?<![\w/])([a-f0-9]{40})(?![\w/])/gi, (sha) => sha.slice(0, 7))
}

function emptyDetail(): ParsedPost {
  return { url: '', number: 0, repo: '', head: '', events: [], body: '', log: '', dropped: 0 }
}

// legacyChips reads the chips of a stored post that has no typed payload. It
// keeps the review of the same head under a plain update, as the feed did.
function legacyChips(details: readonly ParsedPost[]): Chip[] {
  const last = details[details.length - 1]
  if (!last) return []
  const from = !last.head || last.events.some((event) => event.kind !== 'update')
    ? last
    : details.findLast((detail) => detail.head === last.head && detail.events.some((event) => REVIEW_TYPES.has(event.kind as PRType))) ?? last
  return from.events.map((event) => ({ label: event.label, tone: event.tone }))
}
