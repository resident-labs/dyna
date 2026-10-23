<script lang="ts">
  import { api, openExternal } from '../../api.js'
  import { errorText } from '../errorText.ts'
  import type { HeldComment } from '../api/change.ts'

  interface Props {
    comments?: HeldComment[] | null
    roomId: string
    prKey: string
    onchange?: () => void | Promise<void>
  }
  let { comments = [], roomId, prKey, onchange = () => {} }: Props = $props()
  let busy = $state('')
  let error = $state('')
  let admitted = $state<string[]>([])
  let held = $derived((comments ?? []).filter((comment) => !admitted.includes(comment.id)))

  async function admit(comment: HeldComment) {
    busy = comment.id
    error = ''
    try {
      // Like other owner actions in the web app, the server checks authority.
      await api.admitPRComment(roomId, prKey, comment.id)
      admitted = [...admitted, comment.id]
      await onchange()
    } catch (e) {
      error = errorText(e)
    } finally {
      busy = ''
    }
  }
</script>

{#if held.length || error}
  <section aria-label="Held PR comments">
    {#if held.length}
      <p class="hint">Outside comments stay held until you admit them to agent updates.</p>
      {#each held as comment (comment.id)}
        <article>
          <div class="heading">
            <strong>Held: outside commenter {comment.commenter}</strong>
            <button class="admit" disabled={!!busy || !prKey} onclick={() => admit(comment)}>{busy === comment.id ? 'Admitting…' : 'Admit'}</button>
          </div>
          <blockquote>{comment.body}</blockquote>
          {#if comment.url}<button class="source" onclick={() => openExternal(comment.url)}>Source comment ↗</button>{/if}
        </article>
      {/each}
    {/if}
    {#if error}<p class="error" role="alert">Could not admit this comment: {error}</p>{/if}
  </section>
{/if}

<style>
  section { margin: 14px 0; }
  .hint { color: var(--muted); font-size: 12.5px; margin: 0 0 8px; }
  article { margin: 8px 0; padding: 12px; border: 1px solid var(--line-soft); border-radius: 8px; }
  .heading { display: flex; align-items: center; gap: 12px; }
  strong { flex: 1; min-width: 0; font-size: 13px; overflow-wrap: anywhere; }
  .admit { flex: none; padding: 5px 10px; border-radius: 6px; background: var(--raise); color: var(--ink-2); font-size: 12.5px; font-weight: 600; }
  .admit:hover:not(:disabled) { background: var(--raise-hover); color: var(--ink); }
  .admit:disabled { opacity: 0.5; cursor: default; }
  blockquote { margin: 10px 0; padding-left: 12px; border-left: 2px solid var(--line-soft); white-space: pre-wrap; overflow-wrap: anywhere; color: var(--ink-2); font-size: 13px; }
  .source { color: var(--iris); font-size: 12px; }
  .source:hover { text-decoration: underline; }
  .error { color: var(--coral); font-size: 13px; overflow-wrap: anywhere; }
  @media (max-width: 760px) { button { min-height: 44px; } }
</style>
