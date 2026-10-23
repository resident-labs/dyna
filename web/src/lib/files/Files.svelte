<script>
  import { untrack } from 'svelte'
  import { fileURL } from '../../api.js'
  import { human, extOf } from './attach.js'
  import { previewKind, previewLabel } from './preview.ts'
  import VisualBanner from './VisualBanner.svelte'
  import { postAttachment } from '../ui/posts.js'
  import { visuals } from './visuals.svelte.js'
  import { visualOf, updates } from './visuals.js'
  let { roomId, items = [], postId = '', onopen } = $props()
  const open = (attachment) => onopen(postAttachment(attachment, postId))
  const image = (a) => /^image\/(png|jpeg|gif|webp)$/.test(a.mime)
  // Go stamps render "html" on a post's card; a plain .html file is a file.
  const rich = (a) => a.render === 'html'
  // The post that created a visual shows the banner of its newest version;
  // a post that updated it shows a chip for its own version.
  let cards = $derived(items.filter(rich).map((a) => {
    const v = visuals.room === roomId ? visualOf(visuals.byId, postId, a) : null
    // A chip names the visual by its current title, so one visual has one
    // name everywhere.
    if (updates(a)) return { att: v ? { ...a, title: v.attachment.title } : a, chip: true }
    return { att: v?.attachment ?? a, at: v?.post.ts ?? 0, chip: false }
  }))
  let images = $derived(items.filter(image))
  let failedImages = $state(new Set())
  const imageKey = (a) => JSON.stringify([roomId, a.id, a.sha256 || '', fileURL(roomId, a.id)])
  $effect(() => {
    const current = new Set(images.map(imageKey))
    untrack(() => {
      failedImages = new Set([...failedImages].filter((key) => current.has(key)))
    })
  })
  function imageFailed(key) {
    failedImages = new Set(failedImages).add(key)
  }
  let files = $derived(items.filter((a) => !image(a) && !rich(a)))
</script>

{#each cards as c, i (c.att.id + ':' + i)}
  <VisualBanner {roomId} att={c.att} at={c.at} chip={c.chip} onopen={open} />
{/each}

{#if images.length}
  <div class="images" class:many={images.length > 1}>
    {#each images as a, i (imageKey(a) + ':' + i)}
      {@const key = imageKey(a)}
      {#if failedImages.has(key)}
        {@render fileCard(a, true)}
      {:else}
        <button class="img" onclick={() => open(a)} title="{a.name} · {human(a.size)}">
          <img src={fileURL(roomId, a.id)} alt={a.name} loading="lazy" onerror={() => imageFailed(key)} />
        </button>
      {/if}
    {/each}
  </div>
{/if}
{#each files as a, i (a.id + ':' + i)}
  {@render fileCard(a)}
{/each}

{#snippet fileCard(a, unavailable = false)}
  <button class="file" onclick={() => open(a)} title={`Open ${a.name}`}>
    <span class="ext" class:document={['markdown', 'word', 'pdf'].includes(previewKind(a))} class:table={previewKind(a) === 'table'}>{extOf(a.name)}</span>
    <span class="meta">
      <span class="name">{a.name}</span>
      <span class="sub">{unavailable ? 'Preview unavailable' : previewLabel(previewKind(a))} · {human(a.size)}</span>
    </span>
    <span class="open" aria-hidden="true">{unavailable ? 'Open' : '↗'}</span>
  </button>
{/snippet}

<style>
  .images { display: flex; flex-wrap: wrap; gap: 6px; margin: 6px 0 2px; }
  .img {
    display: block;
    border-radius: 8px;
    overflow: hidden;
    background: #18171a;
    max-width: min(420px, 100%);
    line-height: 0;
  }
  .img img { max-width: 100%; max-height: 300px; object-fit: contain; display: block; }
  .many .img { width: 200px; height: 150px; }
  .many .img img { width: 100%; height: 100%; max-height: none; object-fit: cover; }
  .img:hover { outline: 2px solid var(--iris-edge); outline-offset: 1px; }
  .file {
    display: flex;
    align-items: center;
    gap: 12px;
    width: min(420px, 100%);
    margin: 6px 0 2px;
    padding: 10px 12px;
    background: var(--side);
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    text-align: left;
    transition: border-color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .file:hover { border-color: var(--line); background: var(--hover); }
  }
  .file:active { background: var(--select); }
  .ext {
    width: 40px;
    height: 40px;
    flex: none;
    border-radius: 6px;
    background: var(--raise);
    display: grid;
    place-items: center;
    font-family: var(--mono);
    font-size: 10.5px;
    font-weight: 600;
    color: var(--muted);
  }
  .ext.document { color: var(--iris); background: var(--iris-wash); }
  .ext.table { color: var(--sage); background: rgba(151, 201, 149, 0.08); }
  .open { margin-left: auto; color: var(--faint); font-size: 16px; }
  .file .open { flex: none; transition: color var(--motion-fast) var(--ease); }
  @media (hover: hover) {
    .file:hover .open { color: var(--ink); }
  }
  .meta { display: flex; flex-direction: column; min-width: 0; }
  .name { color: var(--hue-1); font-weight: 500; font-size: 14px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sub { color: var(--faint); font-size: 12px; }
</style>
