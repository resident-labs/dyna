<script lang="ts">
  // The card for a worktree event: one quiet line in the feed, and the props a
  // session DM row will pass in PR 3. It reads the typed payload of the post,
  // never its text, and keeps no feed-only state.
  import type { Message, PRNotice } from '../api/room.ts'
  import type { EventSurface } from './eventRenderers.ts'
  import { worktreeEvent } from './eventPayloads.ts'
  import type { WorktreeReason } from './events.gen.ts'
  import { clock } from '../ui/format.ts'

  let { post, notice = post.notice, surface = 'feed' }: { post: Message; notice?: PRNotice; surface?: EventSurface } = $props()

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
  let open = $state(false)
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

<div class="card {surface}" class:open data-id={post.id}>
  <button class="sum" type="button" aria-expanded={open} aria-label={branch ? `Worktree kept ${branch}` : 'Worktree cleanup'} onclick={() => (open = !open)}>
    <span class="glyph" aria-hidden="true">⎇</span>
    <span class="title">{#if branch}<span class="what">Worktree kept</span><span class="branch">{branch}</span>{:else}<span class="what alone">Worktree cleanup</span>{/if}</span>
    {#if ahead > 0}<span class="fact">ahead <b>{ahead}</b></span>{/if}
    <span class="fact dirty">dirty <b>{dirty.length ? dirty.join(', ') : 'none'}</b></span>
    <time title={new Date(post.ts).toLocaleString()}>{clock(post.ts)}</time>
  </button>
  <div class="body">
    {#if path}<p class="path" title={path}>{shortPath}</p>{/if}
    {#if reason}<p class="reason">{reason}</p>{/if}
    <div class="acts">
      {#if path}<button class="btn" type="button" onclick={copyPath}>Copy path</button>{/if}
      <span class="status" role="status">{copyState === 'copied' ? 'Copied' : copyState === 'failed' ? 'Copy failed. Try again.' : ''}</span>
    </div>
  </div>
</div>

<style>
  .card {
    margin: var(--s1) var(--s4);
    border: 1px solid var(--line);
    background: var(--side);
    border-radius: 8px;
    overflow: hidden;
  }
  .card.dm { margin: var(--s1) 0; }
  .sum {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px 12px;
    border: 0;
    background: none;
    color: var(--ink-2);
    font: inherit;
    text-align: left;
    white-space: nowrap;
    overflow: hidden;
    /* The card is always open on a desktop, so only the phone taps to expand. */
    pointer-events: none;
  }
  .glyph { color: var(--gold); font-size: var(--t-ui); flex: none; }
  .title { min-width: 0; overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
  .branch { margin-left: 6px; color: var(--ink-2); font-weight: 600; }
  .fact { color: var(--muted); font-size: var(--t-sm); white-space: nowrap; flex: none; }
  .fact b { color: var(--ink-2); font-weight: 500; }
  /* The clock keeps the row short; the full stamp is in the title. */
  time { margin-left: auto; color: var(--faint); font-size: var(--t-cap); white-space: nowrap; flex: none; padding-left: 4px; }
  .body { display: grid; gap: 6px; padding: 0 12px 10px; }
  .path, .reason { margin: 0; overflow-wrap: anywhere; }
  .path { color: var(--muted); font-family: var(--mono); font-size: var(--t-sm); }
  .reason { color: var(--muted); font-size: var(--t-sm); }
  .acts { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .btn {
    min-height: 28px;
    padding: 3px 10px;
    border: 1px solid var(--line);
    border-radius: 6px;
    background: var(--raise);
    color: var(--ink-2);
    font: inherit;
    font-size: var(--t-sm);
  }
  .status { color: var(--muted); font-size: var(--t-sm); }
  @media (max-width: 760px) {
    .card { margin: var(--s1) var(--s2); }
    .sum { min-height: 40px; padding: 6px var(--s2); cursor: pointer; pointer-events: auto; }
    .branch { margin-left: 4px; }
    /* One row: the glyph and the branch carry the news on a phone, and the
       rest is in the expanded body. */
    .title .what:not(.alone) { display: none; }
    .title .branch { margin-left: 0; }
    .fact.dirty { display: none; }
    .card:not(.open) .body { display: none; }
    .btn { min-height: 36px; }
  }
</style>
