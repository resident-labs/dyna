<script lang="ts">
  import { speaker } from './posts.ts'
  import { markdown, stamp } from './format.ts'
  import { postParts } from './postParts.ts'
  import { postPreview } from './preview.ts'
  import PostFiles from './PostFiles.svelte'
  import PostLink from './PostLink.svelte'
  import type { Attachment, Room } from '../api/room.ts'
  import type { ThreadPost } from '../sessions/dm.ts'

  // A quoted post is the stored snapshot a forward carries, or the post a DM
  // quotes before it is sent.
  type Quoted = ThreadPost

  interface Props {
    room: Pick<Room, 'id' | 'name'>
    post: Quoted
    // author is the original post; it adds the sender's color and label.
    author?: Quoted | null
    onpost?: ((postId: string) => void) | null
    onopenfile?: (attachment: Attachment) => void
    // onremove makes the quote a draft: it drops the label and grows no jump link.
    onremove?: (() => void) | null
    disabled?: boolean
  }
  let { room, post, author = null, onpost = null, onopenfile, onremove = null, disabled = false }: Props = $props()
  let parts = $derived(postParts(post))
  let from = $derived<Quoted>(author ?? post)
  // The quote's own time; a post that is not stored yet has none.
  let at = $derived(post.ts === undefined ? '' : new Date(post.ts).toLocaleString())
  let expanded = $state(false)
  let expandedFor = $state('')
  // A draft folds its post text like the feed does; a posted quote shows it all.
  let preview = $derived(onremove ? postPreview(parts.text) : { text: parts.text, truncated: false })
  // A different forwarded post starts folded again.
  $effect(() => {
    const id = post?.id ?? ''
    if (id === expandedFor) return
    expandedFor = id
    expanded = false
  })
</script>

<div class="fwd" class:draft={!!onremove}>
  <div class="fwdhead">
    <svg viewBox="0 0 20 20" width="14" height="14" aria-hidden="true"><path d="m12 5 5 5-5 5M16.5 10H8a5 5 0 0 0-5 5v1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    <span>{onremove ? 'Forwarding a post ·' : 'Forwarded from'} <span class="hue-{from.fromHue ?? 0}">{speaker(from, room.name)}</span> · <time title={at}>{stamp(post.ts)}</time></span>
    {#if onremove}
      <button class="remove" onclick={onremove} {disabled} aria-label="Remove forwarded post" title="Remove forwarded post">×</button>
    {:else}
      <span class="jump"><PostLink roomId={post.roomId || room.id} postId={post.id} onopen={onpost}>Jump to original</PostLink></span>
    {/if}
  </div>
  {#if parts.text}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (Expanded draft text is a keyboard scroll region.) -->
    <div class="prose fwdtext" class:expanded tabindex={expanded ? 0 : undefined} role={expanded ? 'region' : undefined} aria-label={expanded ? 'Full forwarded post' : undefined}>{@html markdown(expanded ? parts.text : preview.text)}</div>
    {#if preview.truncated}<button class="disclosure" aria-expanded={expanded} onclick={() => (expanded = !expanded)}>{expanded ? 'Show less' : 'Show full post'}</button>{/if}
  {/if}
  {#if parts.attachments.length || post.references?.length}<div class="fwdfiles"><PostFiles roomId={post.roomId || room.id} {post} {parts} onopen={onopenfile} /></div>{/if}
</div>

<style>
  .fwd { grid-column: 2; min-width: 0; margin-top: var(--s1); padding: var(--s1) 0 var(--s1) 10px; border-left: 2px solid #4a474c; }
  .fwdhead { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; font-size: var(--t-ui); color: var(--muted); }
  .fwdhead svg { flex: none; }
  .jump { margin-left: auto; }
  .fwdtext { color: var(--ink-2); font-size: var(--t-body); margin-top: 2px; }
  .fwdfiles { margin-top: var(--s1); }
  .draft { margin: 12px 12px 0; padding: 8px 12px; border: 1px solid var(--line); border-left: 3px solid var(--iris); border-radius: 6px; background: var(--side); }
  .draft .fwdhead { flex-wrap: nowrap; }
  .draft .fwdhead > span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .draft .fwdtext.expanded { max-height: min(50vh, 360px); overflow: auto; overscroll-behavior: contain; }
  .disclosure { min-height: 44px; padding: 6px 0; color: var(--iris); font-size: var(--t-ui); }
  .disclosure:focus-visible, .fwdtext:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: 2px; }
  .remove { flex: none; margin-left: auto; align-self: start; min-width: 28px; min-height: 28px; border-radius: 4px; color: var(--muted); font-size: var(--t-head); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .remove:hover { color: var(--ink); background: var(--hover); } }
  .remove:active { color: var(--ink); background: var(--select); }
  .remove:focus-visible { outline: 2px solid var(--iris-edge); }
  @media (hover: none) { .remove { min-width: 40px; min-height: 40px; } }
</style>
