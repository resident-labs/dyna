// Typed readers for the payload of a sealed system event. They read the
// stored payload only, never the post text, so a card and its event cannot
// disagree. The types come from events.gen.ts, which the Go registry writes.
import { eventSpecs, isHookEvent, isPREvent, isWorktreeEvent } from './events.gen.ts'
import type { EventPayload, HookEvent, PREvent, WorktreeEvent } from './events.gen.ts'

// A post of one event family, with the fields its reader needs. A feed row, a
// stored message and a test fixture all fit it.
export interface EventPost {
  kind?: string
  notice?: { kind: string } | null
  payload?: EventPayload
}

// registeredEvent reports a post whose family and type the registry knows.
export function registeredEvent(post: EventPost): boolean {
  return eventSpecs.some((spec) => spec.family === post.kind && spec.type === post.notice?.kind)
}

// worktreeEvent returns the typed payload of a kept-worktree post.
export function worktreeEvent(post: EventPost): WorktreeEvent | undefined {
  if (post.kind !== 'worktree' || post.notice?.kind !== 'kept') return undefined
  return isWorktreeEvent(post.payload) ? post.payload : undefined
}

// hookEvent returns the typed payload of a hook run post, started or result.
export function hookEvent(post: EventPost): HookEvent | undefined {
  if (post.kind !== 'hook' || (post.notice?.kind !== 'started' && post.notice?.kind !== 'result')) return undefined
  return isHookEvent(post.payload) ? post.payload : undefined
}

// prEvent returns the typed payload of a PR watcher post. A post the watcher
// stored before the typed events existed has no payload, and returns
// undefined; the PR card then reads its stored text (pr.ts).
export function prEvent(post: EventPost): PREvent | undefined {
  if (post.kind !== 'pr') return undefined
  return isPREvent(post.payload) ? post.payload : undefined
}
