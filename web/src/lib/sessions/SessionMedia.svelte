<script lang="ts">
  import { fileURL } from '../../api.js'
  import { dayLabel, short } from '../ui/format.ts'
  import EmptyState from '../ui/EmptyState.svelte'
  import Files from '../files/Files.svelte'
  import PostLink from '../ui/PostLink.svelte'
  import { postAttachment, speaker } from '../ui/posts.ts'
  import type { Attachment, Post, Room } from '../api/room.ts'

  interface Props {
    room: Room
    posts?: Post[]
    onopenfile: (attachment: Attachment) => void
    onpost?: ((postId: string) => void) | null
  }
  let { room, posts = [], onopenfile, onpost = null }: Props = $props()
  const image = (a: Attachment) => /^image\//.test(a.mime || '')
  let days = $derived.by(() => {
    const groups = new Map<string, { key: string; post: Post; att: Attachment }[]>()
    for (const post of posts) {
      if (!post.attachments?.length) continue
      const day = dayLabel(post.ts)
      const items = groups.get(day) ?? []
      for (const [i, att] of (post.attachments || []).entries()) {
        items.push({ key: `${post.id}:${i}`, post, att })
      }
      groups.set(day, items)
    }
    return [...groups].map(([label, items]) => ({ label, items }))
  })
</script>

<div class="media">
  {#if !days.length}
    <EmptyState title="No media">Attachments from this conversation appear here.</EmptyState>
  {/if}
  {#each days as day (day.label)}
    <section aria-label={day.label}>
      <h3>{day.label}</h3>
      <div class="gallery">
        {#each day.items as item (item.key)}
          {@const a = item.att}
          <div class="item" class:document={a.render !== 'html' && !image(a)}>
            {#if a.render === 'html'}
              <button class="tile card" onclick={() => onopenfile(postAttachment(a, item.post.id))} title={`Open ${a.title || a.name}`}>
                <span class="card-icon" aria-hidden="true">&lt;/&gt;</span>
                <span class="card-title">{a.title || a.name}</span>
                <span class="card-kind">HTML card ↗</span>
              </button>
            {:else if image(a)}
              <button class="tile thumbnail" onclick={() => onopenfile(postAttachment(a, item.post.id))} title={`Open ${a.name}`}>
                <img src={fileURL(room.id, a.id)} alt={a.name} loading="lazy" />
              </button>
              <div class="filename" title={a.name}>{a.name}</div>
            {:else}
              <Files postId={item.post.id} roomId={room.id} items={[a]} onopen={onopenfile} />
            {/if}
            <p class="route" title={`From ${item.post.from} to ${(item.post.to || []).join(', ')}`}>
              from {speaker(item.post, room.name)} · to {(item.post.to || []).map((a) => short(a, room.name)).join(', ')}
            </p>
            <PostLink roomId={room.id} postId={item.post.id} onopen={onpost} />
          </div>
        {/each}
      </div>
    </section>
  {/each}
</div>

<style>
  .media { flex: 1; min-height: 0; overflow-y: auto; padding: var(--s5) 28px 40px; }
  section { max-width: 1040px; margin: 0 auto 28px; }
  h3 { color: var(--muted); font-size: var(--t-sm); font-weight: 600; margin: 0 0 14px; padding-bottom: 10px; border-bottom: 1px solid var(--line-soft); }
  .gallery { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 20px var(--s4); }
  .item { min-width: 0; }
  .document { grid-column: 1 / -1; }
  .tile { width: 100%; aspect-ratio: 1; border: 1px solid var(--line-soft); border-radius: 10px; background: var(--side); overflow: hidden; transition: border-color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .tile:hover { border-color: var(--iris-edge); }
  }
  .thumbnail { display: block; padding: 0; }
  .thumbnail img { display: block; width: 100%; aspect-ratio: 1; object-fit: cover; }
  .card { display: flex; flex-direction: column; justify-content: center; align-items: center; gap: var(--s3); padding: 18px; text-align: center; }
  .card-icon { font-family: var(--mono); font-size: calc(var(--t-display) * 1.25); color: var(--iris); }
  .card-title { color: var(--ink); font-size: var(--t-body); font-weight: 600; overflow-wrap: anywhere; }
  .card-kind { color: var(--muted); font-size: var(--t-cap); }
  .filename { color: var(--ink-2); font-size: var(--t-sm); margin-top: var(--s2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .route { font-size: var(--t-cap); color: var(--muted); margin: 6px 0 0; overflow-wrap: anywhere; }
  @media (max-width: 760px) { .media { padding: 18px var(--s4); } .gallery { grid-template-columns: repeat(auto-fill, minmax(130px, 1fr)); gap: var(--s4) var(--s3); } }
</style>
