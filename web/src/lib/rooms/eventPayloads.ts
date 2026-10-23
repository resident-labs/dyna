// Typed readers for the payload of a sealed system event. They read the
// stored payload only, never the post text, so a card and its event cannot
// disagree. The types come from events.gen.ts, which the Go registry writes.
import { isHookEvent, isWorktreeEvent } from './events.gen.ts'
import type { HookEvent, WorktreeEvent } from './events.gen.ts'
import type { Message } from '../api/room.ts'

// worktreeEvent returns the typed payload of a kept-worktree post.
export function worktreeEvent(post: Pick<Message, 'kind' | 'notice' | 'payload'>): WorktreeEvent | undefined {
  if (post.kind !== 'worktree' || post.notice?.kind !== 'kept') return undefined
  return isWorktreeEvent(post.payload) ? post.payload : undefined
}

// hookEvent returns the typed payload of a hook run post, started or result.
export function hookEvent(post: Pick<Message, 'kind' | 'notice' | 'payload'>): HookEvent | undefined {
  if (post.kind !== 'hook' || (post.notice?.kind !== 'started' && post.notice?.kind !== 'result')) return undefined
  return isHookEvent(post.payload) ? post.payload : undefined
}
