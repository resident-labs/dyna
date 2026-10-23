<script lang="ts" generics="P extends { id: string; text?: string }">
  // The action bar every post owns: Reply, Forward, Copy text, Copy link, Pin.
  // show comes from the view's one selected post (postSelect.ts), so a post
  // never shows two bars and two posts never show bars at once. All icons
  // share one 18 px grid.
  import { copyText } from '../../api.js'
  import PinIcon from './PinIcon.svelte'
  import { postHref } from './posts.js'
  import type { Room } from '../api/room.ts'

  interface Props {
    room: Pick<Room, 'id'>
    p: P
    pinned?: boolean
    show?: boolean
    onreply?: (post: P) => void
    onforward?: ((post: P) => void) | null
    onpin?: (post: P) => void
  }
  let { room, p, pinned = false, show = false, onreply, onforward, onpin }: Props = $props()

  // Keep feedback local to the selected post.
  let copied = $state('')
  let copyError = $state(false)
  let generation = 0
  $effect(() => {
    p
    generation++
    copied = ''
    copyError = false
    return () => clearTimeout(timer)
  })
  let timer: ReturnType<typeof setTimeout> | undefined
  async function copy(which: 'text' | 'link') {
    const current = ++generation
    clearTimeout(timer)
    copied = ''
    copyError = false
    try {
      await copyText(which === 'text' ? p.text || '' : new URL(postHref(room.id, p.id), location.href).href)
      if (current !== generation) return
      copied = which
    } catch {
      if (current !== generation) return
      copyError = true
    }
    timer = setTimeout(() => { copied = ''; copyError = false }, 2000)
  }
</script>

<div class="actions" class:show>
  <span class="copy-status" class:error={copyError} role="status">{copyError ? 'Could not copy' : copied ? 'Copied' : ''}</span>
  {#if onreply}<button onclick={() => onreply(p)} title="Reply" aria-label="Reply">
    <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M8 5 3 10l5 5M3.5 10H12a5 5 0 0 1 5 5v1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
  </button>{/if}
  {#if onforward}<button onclick={() => onforward(p)} title="Forward" aria-label="Forward">
    <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="m12 5 5 5-5 5M16.5 10H8a5 5 0 0 0-5 5v1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
  </button>{/if}
  <button class:ok={copied === 'text'} onclick={() => copy('text')} title="Copy text" aria-label="Copy text">
    {#if copied === 'text'}
      <svg class="swap" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="m4 10.5 4 4 8-9" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg><span class="done" aria-hidden="true">Copied</span>
    {:else}
      <svg class="swap" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><rect x="7" y="7" width="9" height="9" rx="2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M13 7V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    {/if}
  </button>
  <button class:ok={copied === 'link'} onclick={() => copy('link')} title="Copy link" aria-label="Copy link">
    {#if copied === 'link'}
      <svg class="swap" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="m4 10.5 4 4 8-9" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg><span class="done" aria-hidden="true">Copied</span>
    {:else}
      <svg class="swap" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"><path d="M8.5 11.5a3 3 0 0 0 4.2 0l2.6-2.6a3 3 0 0 0-4.2-4.2l-.8.8M11.5 8.5a3 3 0 0 0-4.2 0l-2.6 2.6a3 3 0 0 0 4.2 4.2l.8-.8" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    {/if}
  </button>
  {#if onpin}<button class="pin" class:on={pinned} onclick={() => onpin(p)} title={pinned ? 'Unpin' : 'Pin'} aria-label={pinned ? 'Unpin' : 'Pin'} aria-pressed={pinned}>
    <span class="tack"><PinIcon filled={pinned} /></span>
  </button>{/if}
</div>

<style>
  .copy-status { position: absolute; right: 0; top: calc(100% + 4px); white-space: nowrap; font-size: var(--t-ui); color: var(--sage); background: var(--side); border-radius: 4px; }
  .copy-status:not(:empty) { padding: 4px 8px; }
  .copy-status.error { color: var(--coral); }
  .actions {
    position: absolute;
    right: 16px;
    top: -14px;
    z-index: 2;
    display: flex;
    max-width: calc(100% - 32px);
    flex-wrap: wrap;
    padding: 2px;
    gap: 2px;
    background: var(--side);
    border: 1px solid var(--line);
    border-radius: 8px;
    box-shadow: 0 4px 14px rgba(0, 0, 0, 0.3);
    /* Hidden, but laid out: showing it never moves the post. */
    opacity: 0;
    visibility: hidden;
    pointer-events: none;
    transform: translateY(4px);
    transition: opacity var(--motion-fast) var(--ease), transform var(--motion-fast) var(--ease), visibility 0s linear var(--motion-fast);
  }
  .actions.show { opacity: 1; visibility: visible; pointer-events: auto; transform: none; transition-delay: 0s; }
  :global(.post.swipe-drag) .actions { opacity: 0; visibility: hidden; }
  @media (hover: none) { .actions { top: -18px; } }
  button {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    min-width: 28px;
    height: 26px;
    padding: 0 5px;
    font-size: var(--t-ui);
    color: var(--muted);
    border-radius: 6px;
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease), transform var(--motion-fast) var(--ease);
  }
  @media (hover: none) { button { min-width: 40px; height: 40px; } }
  @media (hover: hover) {
    button:hover { color: var(--ink); background: var(--hover); }
  }
  button:active { transform: scale(0.92); background: var(--select); }
  button:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: 0; color: var(--ink); }
  button.ok { color: var(--sage); }
  .done { padding-right: 3px; animation: fade-in var(--motion-fast) var(--ease); }
  .swap { animation: pop var(--motion) var(--ease); }
  .tack { display: flex; transition: transform var(--motion) var(--ease); transform: rotate(0deg); }
  .pin.on { color: var(--gold); }
  .pin.on .tack { transform: rotate(-35deg); }
  .pin:active .tack { transform: rotate(-35deg) scale(0.9); }
  @keyframes fade-in { from { opacity: 0; } }
  @keyframes pop { from { opacity: 0; transform: scale(0.7); } }
  @media (prefers-reduced-motion: reduce) {
    .actions, .actions button, .tack { transform: none !important; }
  }
</style>
