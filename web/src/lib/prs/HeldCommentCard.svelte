<script lang="ts">
  import { api, openExternal } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import type { HeldComment } from '../api/change.ts'
  import { removeHeldComment } from './heldComments.ts'

  let { comment }: { comment: HeldComment } = $props()
  let busy = $state('')
  let error = $state('')

  async function resolve(action: 'admit' | 'dismiss') {
    busy = action
    error = ''
    try {
      // The server checks owner authority for both actions.
      if (action === 'admit') await api.admitPRComment(comment.roomId, comment.key, comment.id, comment.version)
      else await api.dismissPRComment(comment.roomId, comment.key, comment.id)
      removeHeldComment(comment)
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = ''
    }
  }
</script>

<article aria-label="Held PR comment">
  <strong>Held: outside commenter {comment.commenter} · PR #{comment.number}</strong>
  <blockquote>{comment.body}</blockquote>
  <div class="actions">
    <button class="admit" disabled={!!busy} onclick={() => resolve('admit')}>{busy === 'admit' ? 'Admitting…' : 'Admit'}</button>
    <button class="dismiss" disabled={!!busy} onclick={() => resolve('dismiss')}>{busy === 'dismiss' ? 'Dismissing…' : 'Dismiss'}</button>
    {#if comment.url}<button class="source" onclick={() => openExternal(comment.url)}>Source comment ↗</button>{/if}
  </div>
  {#if error}<p class="error" role="alert">Could not {busy || 'resolve'} this comment: {error}</p>{/if}
</article>

<style>
  article { margin: 8px 0; padding: 12px; border: 1px solid var(--line-soft); border-radius: 8px; }
  strong { font-size: 13px; overflow-wrap: anywhere; }
  .actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
  .admit, .dismiss { padding: 5px 10px; border-radius: 6px; background: var(--raise); color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
  .admit:hover:not(:disabled), .dismiss:hover:not(:disabled) { background: var(--raise-hover); color: var(--ink); }
  button:disabled { opacity: 0.5; cursor: default; }
  blockquote { margin: 10px 0; padding-left: 12px; border-left: 2px solid var(--line-soft); white-space: pre-wrap; overflow-wrap: anywhere; color: var(--ink-2); font-size: 13px; }
  .source { color: var(--iris); font-size: 12px; }
  .source:hover { text-decoration: underline; }
  .error { color: var(--coral); font-size: 13px; overflow-wrap: anywhere; }
  @media (max-width: 760px) { button { min-height: 44px; } }
</style>
