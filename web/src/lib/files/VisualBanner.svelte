<script lang="ts">
  import { fileURL } from '../../api.ts'
  import { ago } from '../ui/format.ts'
  import VisualPlaceholder from './VisualPlaceholder.svelte'
  import type { Attachment } from '../api/room.ts'

  interface Props {
    roomId: string
    att: Attachment
    at?: number
    chip?: boolean
    onopen: (attachment: Attachment) => void
  }
  // A visualization in chat: the banner image of its newest version, its
  // title, version and when it last changed. A post that updated a visual
  // shows a one-line chip for its own version instead. Checks never show
  // here; the full view has them.
  let { roomId, att, at = 0, chip = false, onopen }: Props = $props()
  let title = $derived(att.title || att.name)
  let loaded = $state(false)
  let failed = $state(false)
  let image = $derived(chip ? att.thumb : att.banner)
  $effect(() => {
    image
    loaded = false
    failed = false
  })
</script>

{#if chip}
  <button class="chip" onclick={() => onopen(att)} title="Open {title} v{att.version}">
    <span class="mini">
      {#if image && !failed}<img src={fileURL(roomId, image)} alt="" loading="lazy" decoding="async" onerror={() => (failed = true)} />{:else}<VisualPlaceholder title="" />{/if}
    </span>
    <span class="say">Updated <strong>{title}</strong> to v{att.version}</span>
  </button>
{:else}
  <button class="banner" onclick={() => onopen(att)} aria-label="Open {title}, version {att.version || 1}">
    <span class="art" class:ready={loaded || !image || failed}>
      {#if image && !failed}
        <img src={fileURL(roomId, image)} alt="" loading="lazy" decoding="async" onload={() => (loaded = true)} onerror={() => (failed = true)} />
      {:else}
        <VisualPlaceholder {title} />
      {/if}
    </span>
    <span class="row">
      <span class="title">{title}</span>
      <span class="pill">v{att.version || 1}</span>
      {#if at}<time>updated {ago(new Date(at).toISOString())}</time>{/if}
    </span>
  </button>
{/if}

<style>
  .banner {
    display: block;
    width: min(560px, 100%);
    margin: 6px 0 2px;
    text-align: left;
    background: var(--side);
    border: 1px solid var(--line-soft);
    border-radius: 10px;
    overflow: hidden;
    transition: border-color var(--motion-fast) var(--ease), transform var(--motion) var(--ease);
  }
  .art {
    position: relative;
    display: block;
    aspect-ratio: 8 / 3;
    overflow: hidden;
    background: linear-gradient(90deg, var(--raise) 0%, var(--raise-hover) 50%, var(--raise) 100%) 0 0 / 200% 100%;
    animation: shimmer 1.4s ease-in-out infinite;
  }
  .art.ready { animation: none; background: var(--raise); }
  /* A soft fade joins the picture to the title row. */
  .art::after {
    content: '';
    position: absolute;
    inset: auto 0 0 0;
    height: 28%;
    background: linear-gradient(transparent, var(--side));
    pointer-events: none;
  }
  img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
    object-position: top;
    opacity: 0;
    transition: opacity var(--motion-slow) var(--ease), transform var(--motion-slow) var(--ease);
  }
  .ready img { opacity: 1; }
  .row { display: flex; align-items: center; gap: 8px; padding: 10px 14px 12px; min-width: 0; }
  .title { font-size: 14.5px; font-weight: 600; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .pill { flex: none; font: 600 11px var(--mono); color: var(--iris); background: var(--iris-wash); padding: 1px 7px; border-radius: 9px; }
  time { flex: none; margin-left: auto; color: var(--faint); font-size: 12px; white-space: nowrap; }
  @media (hover: hover) {
    .banner:hover { border-color: var(--line); transform: translateY(-1px); }
    .banner:hover img { transform: scale(1.02); }
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    max-width: 100%;
    margin: 6px 0 2px;
    padding: 5px 12px 5px 5px;
    background: var(--side);
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    color: var(--ink-2);
    font-size: 13px;
    transition: border-color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  .mini { flex: none; width: 48px; height: 36px; border-radius: 5px; overflow: hidden; background: var(--raise); }
  .mini img { opacity: 1; object-fit: cover; }
  .say { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .say strong { color: var(--ink); font-weight: 600; }
  @media (hover: hover) { .chip:hover { border-color: var(--line); background: var(--hover); } }
  .chip:active { background: var(--select); }
  @keyframes shimmer { from { background-position: 100% 0; } to { background-position: -100% 0; } }
  @media (prefers-reduced-motion: reduce) {
    .art { animation: none; }
    .banner, img { transition: none; }
    .banner:hover, .banner:hover img { transform: none; }
  }
</style>
