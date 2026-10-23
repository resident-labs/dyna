<script lang="ts">
  import type { Attachment, Post, PostDetail, Room } from '../api/room.ts'
  import { speaker } from '../ui/posts.ts'
  import Avatar from '../ui/Avatar.svelte'
  import PostFiles from '../ui/PostFiles.svelte'
  import { postParts } from '../ui/postParts.ts'
  import CompletionBody from '../ui/CompletionBody.svelte'
  import { markdown, stamp, ago } from '../ui/format.ts'

  // The Pinned panel. It takes the member list's place, like Discord. Each
  // item reuses Files, so attachments and HTML cards render as in the feed.
  interface Props {
    room: Room
    pins?: Post[]
    onjump: (id: string) => void
    onunpin: (post: Post) => unknown
    onopenfile?: (attachment: Attachment) => void
    onclose: () => void
    ondetail: (detail: PostDetail) => void
  }
  let { room, pins = [], onjump, onunpin, onopenfile, onclose, ondetail }: Props = $props()

  // The label comes from Go (roomsvc.SpeakerLabel), as in the feed.
  const who = (p: Post): string => speaker(p, room.name)
  let query = $state('')
  let needle = $derived(query.trim().toLocaleLowerCase())
  let filtered = $derived(pins.filter(p => !needle || [postParts(p).text, who(p),
    ...postParts(p).attachments.flatMap(file => [file.name, file.title]), ...postParts(p).names]
    .some(value => String(value || '').toLocaleLowerCase().includes(needle))))
</script>

<aside class="pins" aria-label="Pinned posts">
  <header>
    <h2>Pinned <span class="n">{pins.length}</span></h2>
    <button class="x" onclick={onclose} title="Close (Esc)" aria-label="Close pinned posts">✕</button>
  </header>
  {#if pins.length}
    <div class="filter">
      <div class="filter-field"><input type="search" aria-label="Filter pinned posts" placeholder="Filter pinned posts" bind:value={query} />
        {#if query}<button onclick={() => query = ''} aria-label="Clear filter">Clear</button>{/if}
      </div>
      <p class="matches" role="status">{filtered.length} of {pins.length} pinned posts</p>
    </div>
  {/if}
  <div class="scroll">
    {#if pins.length === 0}
      <p class="none">No pinned posts. <span class="mouse">Hover a post and press Pin to keep it here.</span><span class="touch">Tap a post and press Pin to keep it here.</span></p>
    {/if}
    {#if pins.length && !filtered.length}<p class="none">No pinned posts match this filter.</p>{/if}
    {#each filtered as p (p.id)}
      <article class="item">
        <div class="head">
          <Avatar name={who(p)} hue={p.fromHue} owner={p.owner} emoji={p.fromEmoji} size={32} ring="var(--side)" />
          <span class="who">
            <span class="name hue-{p.fromHue}">{who(p)}</span>
            <time title={new Date(p.ts).toLocaleString()}>{stamp(p.ts)}</time>
          </span>
        </div>
        {#if p.completion}<div class="prose"><CompletionBody post={p} onopen={ondetail} /></div>
        {:else if postParts(p).text}<div class="prose">{@html markdown(postParts(p).text)}</div>{/if}
        <PostFiles roomId={room.id} post={p} onopen={onopenfile} />
        <footer>
          <span class="by" title={p.pinnedBy}>pinned {ago(new Date(p.pinnedAt ?? 0).toISOString())}</span>
          <button onclick={() => onjump(p.id)}>Jump</button>
          <button class="danger" onclick={() => onunpin(p)}>Unpin</button>
        </footer>
      </article>
    {/each}
  </div>
</aside>

<style>
  .pins {
    width: 360px;
    flex: none;
    background: var(--side);
    display: flex;
    flex-direction: column;
    min-height: 0;
    border-left: 1px solid var(--line-soft);
  }
  header { flex: none; display: flex; align-items: center; padding: 14px var(--s3) var(--s2) var(--s4); }
  h2 { flex: 1; margin: 0; font-size: var(--t-lead); font-weight: 600; display: flex; gap: 6px; align-items: baseline; }
  .n { color: var(--faint); font-weight: 500; font-size: var(--t-ui); }
  .x { color: var(--muted); width: 28px; height: 28px; border-radius: 6px; font-size: var(--t-ui); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .x:hover { color: var(--ink); background: var(--hover); }
  }
  .x:active {
    background: var(--select);
}
  .filter { padding: var(--s2) var(--s3); }
  .filter-field { display: flex; gap: var(--s2); align-items: center; }
  .filter-field input { width: 100%; min-width: 0; background: var(--field); border: 1px solid var(--line); border-radius: 6px; padding: 8px; color: var(--ink); font: inherit; }
  .filter-field button { padding: 6px; color: var(--iris); }
  .matches { color: var(--muted); font-size: var(--t-sm); margin: var(--s2) 0 0; }
  .scroll { flex: 1; overflow-y: auto; padding: var(--s1) 10px var(--s3); }
  .none { margin: var(--s2) 6px; font-size: var(--t-ui); color: var(--faint); line-height: var(--leading); }
  /* A phone has no hover, so its hint names the tap that shows Pin. */
  .none .touch { display: none; }
  @media (hover: none) {
    .none .mouse { display: none; }
    .none .touch { display: inline; }
  }
  .item {
    background: var(--main);
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    padding: 10px var(--s3) var(--s2);
    margin-bottom: 10px;
    min-width: 0;
  }
  .head { display: flex; align-items: center; gap: 10px; margin-bottom: var(--s1); }
  .who { display: flex; flex-direction: column; min-width: 0; line-height: var(--leading-tight); }
  .name { font-weight: 600; font-size: var(--t-body); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  time { font-size: var(--t-sm); color: var(--faint); }
  .prose { font-size: var(--t-body); overflow-wrap: anywhere; }
  footer { display: flex; align-items: center; gap: var(--s1); margin-top: var(--s2); padding-top: 6px; border-top: 1px solid var(--line-soft); }
  .by { flex: 1; font-size: var(--t-sm); color: var(--faint); }
  footer button { font-size: var(--t-sm); color: var(--muted); padding: 3px 9px; border-radius: 5px; background: var(--raise); transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    footer button:hover { color: var(--ink); background: var(--hover); }
  }
  footer button:active {
    background: var(--select);
}
  footer button.danger { transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    footer button.danger:hover { color: var(--coral); }
  }
</style>
