<script>
  import { tick } from 'svelte'
  import { speaker } from './posts.ts'
  import { markdown, stamp } from './format.ts'
  import { postParts } from './postParts.ts'
  import PostFiles from './PostFiles.svelte'
  import PostLink from './PostLink.svelte'

  let { room, post, author = null, onpost = null, onopenfile, onremove = null, disabled = false } = $props()
  let parts = $derived(postParts(post))
  let from = $derived(author ?? post)
  let textEl = $state()
  let expanded = $state(false)
  let overflowing = $state(false)
  $effect(() => {
    post
    parts.text
    const node = textEl
    const draft = !!onremove
    expanded = false
    overflowing = false
    if (!node || !draft) return
    let active = true
    const measure = () => {
      if (!active) return
      // Measure the existing three-line clamp even while the text is expanded.
      node.classList.add('measure')
      overflowing = node.scrollHeight > node.clientHeight + 1
      node.classList.remove('measure')
      if (!overflowing) expanded = false
    }
    const observer = new ResizeObserver(measure)
    observer.observe(node)
    tick().then(measure)
    return () => { active = false; observer.disconnect() }
  })
</script>

<div class="fwd" class:draft={!!onremove}>
  <div class="fwdhead">
    <svg viewBox="0 0 20 20" width="14" height="14" aria-hidden="true"><path d="m12 5 5 5-5 5M16.5 10H8a5 5 0 0 0-5 5v1" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
    <span>{onremove ? 'Forwarding a post ·' : 'Forwarded from'} <span class="hue-{from.fromHue ?? 0}">{speaker(from, room.name)}</span> · <time title={new Date(post.ts).toLocaleString()}>{stamp(post.ts)}</time></span>
    {#if onremove}
      <button class="remove" onclick={onremove} {disabled} aria-label="Remove forwarded post" title="Remove forwarded post">×</button>
    {:else}
      <span class="jump"><PostLink roomId={post.roomId || room.id} postId={post.id} onopen={onpost}>Jump to original</PostLink></span>
    {/if}
  </div>
  {#if parts.text}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (Expanded draft text is a keyboard scroll region.) -->
    <div class="prose fwdtext" class:expanded bind:this={textEl} tabindex={expanded ? 0 : undefined} role={expanded ? 'region' : undefined} aria-label={expanded ? 'Full forwarded post' : undefined}>{@html markdown(parts.text)}</div>
    {#if onremove && overflowing}<button class="disclosure" aria-expanded={expanded} onclick={() => expanded = !expanded}>{expanded ? 'Show less' : 'Show full post'}</button>{/if}
  {/if}
  {#if parts.attachments.length}<div class="fwdfiles"><PostFiles roomId={post.roomId || room.id} {post} {parts} onopen={onopenfile} /></div>{/if}
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
  .draft .fwdtext { display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
  .draft .fwdtext.expanded { display: block; max-height: min(50vh, 360px); overflow: auto; -webkit-line-clamp: unset; line-clamp: unset; overscroll-behavior: contain; }
  .draft .fwdtext.measure { display: -webkit-box; max-height: none; overflow: hidden; -webkit-line-clamp: 3; line-clamp: 3; }
  .disclosure { min-height: 44px; padding: 6px 0; color: var(--iris); font-size: var(--t-ui); }
  .disclosure:focus-visible, .fwdtext:focus-visible { outline: 2px solid var(--iris-edge); outline-offset: 2px; }
  .remove { flex: none; margin-left: auto; align-self: start; min-width: 28px; min-height: 28px; border-radius: 4px; color: var(--muted); font-size: var(--t-head); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) { .remove:hover { color: var(--ink); background: var(--hover); } }
  .remove:active { color: var(--ink); background: var(--select); }
  .remove:focus-visible { outline: 2px solid var(--iris-edge); }
  @media (hover: none) { .remove { min-width: 40px; min-height: 40px; } }
</style>
