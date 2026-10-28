import type { EventPost } from './eventRenderers.ts'
import { turnEvent } from './eventPayloads.ts'
import { turnText } from '../ui/format.ts'

// Use the same displayed result and session for message actions on a turn.
export function turnMessage<P extends EventPost>(post: P): P & { from: string; text: string } {
  const payload = turnEvent(post)
  return {
    ...post,
    from: payload?.session ?? post.from ?? '',
    text: turnText(post.fullResult && post.text ? post.text : payload?.summary ?? '') || payload?.error || '',
  }
}
