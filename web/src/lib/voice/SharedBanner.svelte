<script lang="ts">
  // The newest file shared in the call, as one slim row under the call's
  // header: a thumbnail, its title and who shared it. A tap opens the
  // full view; × hides it. The call's thread keeps every shared file.
  import { fileURL } from '../../api.ts'
  import type { Attachment, Post } from '../api/room.ts'
  import { extOf } from '../files/attach.ts'
  import VisualPlaceholder from '../files/VisualPlaceholder.svelte'
  import { NAMES, shareKind, shareTitle, shareWhat } from './conversation.ts'
  import { icons } from './icons.ts'

  // Share is one file shared in the call, as sharedItems lists it: its key,
  // its post's ID and time, who shared it, and the attachment.
  interface Share {
    key: string
    ts: Post['ts']
    postId: Post['id']
    who: keyof typeof NAMES
    att: Attachment
  }

  let { roomId, share, onopen, ondismiss }: {
    roomId: string
    share: Share
    onopen: (share: Share) => void
    ondismiss: (share: Share) => void
  } = $props()

  let kind = $derived(shareKind(share.att))
  let title = $derived(shareTitle(share.att))
  let image = $derived(kind === 'image' ? share.att.id : kind === 'card' ? share.att.thumb : '')
  let failed = $state(false)
  $effect(() => {
    image
    failed = false
  })
</script>

<div class="shared">
  <button class="open" onclick={() => onopen(share)} aria-label="Open {title}, shared by {NAMES[share.who]}">
    <span class="thumb">
      {#if image && !failed}
        <img src={fileURL(roomId, image)} alt="" decoding="async" onerror={() => (failed = true)} />
      {:else if kind === 'card'}
        <VisualPlaceholder title="" />
      {:else}
        <span class="ext">{extOf(share.att.name)}</span>
      {/if}
    </span>
    <span class="text">
      <strong>{title}</strong>
      <span>{NAMES[share.who]} shared {shareWhat(share.att)} · open</span>
    </span>
  </button>
  <button class="hide" aria-label="Hide {title}" title="Hide" onclick={() => ondismiss(share)}>
    <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><path d={icons.close} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
  </button>
</div>

<style>
  .shared { display: flex; align-items: center; gap: 2px; border: 1px solid var(--line); border-radius: var(--r-sm); background: var(--panel); }
  .open { flex: 1; min-width: 0; min-height: 48px; display: flex; align-items: center; gap: 10px; padding: 6px 4px 6px 8px; text-align: left; }
  .thumb { flex: none; width: 48px; height: 34px; border-radius: 5px; overflow: hidden; display: grid; place-items: center; background: var(--raise); border: 1px solid var(--line-soft); }
  .thumb img { width: 100%; height: 100%; object-fit: cover; }
  .ext { font-size: var(--t-cap); font-weight: 600; color: var(--muted); text-transform: uppercase; }
  .text { min-width: 0; display: flex; flex-direction: column; line-height: var(--leading-tight); }
  .text strong { font-size: var(--t-ui); font-weight: 600; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .text span { font-size: var(--t-sm); color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .hide { flex: none; width: 40px; height: 40px; display: grid; place-items: center; color: var(--muted); border-radius: var(--r-xs); transition: background-color var(--motion-fast) var(--ease), color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .open:hover strong { text-decoration: underline; }
    .hide:hover { color: var(--ink); background: var(--raise-hover); }
  }
  .hide:active { background: var(--select); }
</style>
