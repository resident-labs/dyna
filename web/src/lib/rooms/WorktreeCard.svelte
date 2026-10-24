<script lang="ts">
  // The card for a worktree event: one quiet line in the feed, and the props a
  // session DM row will pass in PR 3. It reads the typed payload of the post,
  // never its text, and keeps no feed-only state.
  import type { EventNotice } from '../api/room.ts'
  import type { EventPost, EventSurface } from './eventRenderers.ts'
  import { worktreeEvent } from './eventPayloads.ts'
  import type { WorktreeReason } from './events.gen.ts'
  import EventDetails from './EventDetails.svelte'
  import EventRow from './EventRow.svelte'

  let { post, notice = post.notice, surface = 'feed' }: { post: EventPost; notice?: EventNotice; surface?: EventSurface } = $props()

  const reasonLabel: Record<WorktreeReason, string> = {
    'not-merged': 'HEAD is not merged into origin/main, so cleanup kept it.',
    dirty: 'The tree has uncommitted changes or untracked files, so cleanup kept it.',
    'manual-handoff': 'The tree is kept for a manual handoff.',
    'keep-failed': 'Cleanup could not keep the session trees.',
    other: 'Cleanup kept the tree.',
  }

  let payload = $derived(worktreeEvent(post))
  let path = $derived(payload?.path ?? '')
  let branch = $derived(payload?.branch ?? '')
  let ahead = $derived(payload?.ahead ?? 0)
  let dirty = $derived(payload?.dirty ?? [])
  let reason = $derived(payload ? payload.detail || reasonLabel[payload.reason] : '')
  let copyState = $state<'' | 'copied' | 'failed'>('')

  // The two last segments identify a tree; the full path is one tap away.
  let shortPath = $derived.by(() => {
    const parts = path.split('/').filter(Boolean)
    return parts.length > 2 ? '…/' + parts.slice(-2).join('/') : path
  })

  async function copyPath() {
    if (!path || copyState === 'copied') return
    try {
      await navigator.clipboard.writeText(path)
      copyState = 'copied'
    } catch {
      copyState = 'failed'
    }
  }
</script>

<EventRow
  id={post.id}
  ts={post.ts}
  {surface}
  title={branch ? 'Worktree kept' : 'Worktree cleanup'}
  chips={[{ label: 'kept', tone: 'gold' }]}
  summary={[branch, ahead > 0 ? `ahead ${ahead}` : '', `dirty ${dirty.length ? dirty.join(', ') : 'none'}`].filter(Boolean).join(' · ')}
>
  {#snippet icon()}⎇{/snippet}
  {#snippet detail()}
  <div class="body">
    {#if path}<p class="path" title={path}>{shortPath}</p>{/if}
    {#if reason}<p class="reason">{reason}</p>{/if}
    <div class="acts">
      {#if path}<button class="btn" type="button" onclick={copyPath}>Copy path</button>{/if}
      <span class="status" role="status">{copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed. Try again.' : ''}</span>
    </div>
    {#if surface === 'dm'}<EventDetails text={post.text} />{/if}
  </div>
  {/snippet}
</EventRow>

<style>
  .body { display: grid; gap: 6px; }
  .path, .reason { margin: 0; overflow-wrap: anywhere; }
  .path { color: var(--muted); font-family: var(--mono); font-size: var(--t-sm); }
  .reason { color: var(--muted); font-size: var(--t-sm); }
  .acts { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .status { color: var(--muted); font-size: var(--t-sm); }
</style>
