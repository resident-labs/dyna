<script>
  import { human, extOf } from './attach.js'
  import { dialog } from '../ui/dialog.ts'
  let { items = [], onremove, onretry } = $props()
  const titleId = $props.id()
  let previewKey = $state(null)
  let preview = $derived(items.find(it => it.key === previewKey))
  // Leave transformed, filtered, or clipped composer ancestors behind.
  function portal(node) {
    document.body.append(node)
    return { destroy: () => node.remove() }
  }
  const closePreview = () => { previewKey = null }
  $effect(() => {
    if (previewKey !== null && !preview) closePreview()
  })
</script>

{#if items.length}
  <div class="tray" aria-label="Attachments">
    {#each items as it (it.key)}
      <div class="chip" class:failed={it.status === 'failed'} class:loading={it.status === 'uploading'}>
        {#if it.image && it.url}
          <button class="thumbnail" onclick={() => previewKey = it.key} aria-label="Preview {it.name}"><img src={it.url} alt="" /></button>
        {:else}
          <span class="ext">{extOf(it.name)}</span>
        {/if}
        <span class="meta">
          <span class="name" title={it.name}>{it.name}</span>
          <span class="sub">
            {#if it.status === 'uploading'}saving…{:else if it.status === 'failed'}{it.error || 'failed'}{:else}{human(it.size)}{/if}
          </span>
        </span>
        {#if it.status === 'failed'}
          <button class="act" onclick={() => onretry(it)} title="Try again">Retry</button>
        {/if}
        <button class="x" onclick={() => onremove(it)} aria-label="Remove {it.name}">
          <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
        </button>
      </div>
    {/each}
  </div>
{/if}

{#if preview?.image && preview.url}
  <div class="preview-backdrop" use:portal>
    <div class="preview" use:dialog={{ close: closePreview }} role="dialog" aria-modal="true" aria-labelledby={titleId} tabindex="-1">
      <header>
        <h2 id={titleId}>{preview.name}</h2>
        <button class="close" onclick={closePreview} aria-label="Close preview">Close</button>
      </header>
      <img class="preview-image" src={preview.url} alt={preview.name} />
    </div>
  </div>
{/if}

<style>
  .preview-backdrop { position: fixed; inset: 0; z-index: 60; padding: var(--s4); background: rgba(8, 8, 10, 0.72); display: grid; place-items: center; }
  .preview { width: min(960px, 100%); max-height: calc(100dvh - 32px); display: flex; flex-direction: column; gap: var(--s3); padding: var(--s4); border: 1px solid var(--line); border-radius: var(--r-lg); background: var(--side); outline: none; }
  .preview header { display: flex; align-items: center; gap: var(--s3); }
  .preview h2 { flex: 1; min-width: 0; margin: 0; font-size: var(--t-title); overflow-wrap: anywhere; }
  .close { min-height: 44px; padding: 0 var(--s3); border-radius: var(--r-xs); background: var(--raise); font-size: var(--t-ui); }
  .preview-image { width: 100%; min-height: 0; max-height: calc(100dvh - 140px); object-fit: contain; }
  .thumbnail { flex: none; padding: 0; border-radius: 5px; }

  .tray {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    padding: 10px 12px 2px;
  }
  .chip {
    position: relative;
    display: flex;
    align-items: center;
    gap: 10px;
    width: 220px;
    padding: 8px 48px 8px 8px;
    background: var(--side);
    border: 1px solid var(--line-soft);
    border-radius: 8px;
  }
  .chip.failed { border-color: color-mix(in srgb, var(--coral) 45%, transparent); }
  .chip.loading { opacity: 0.7; }
  .thumbnail img {
    width: 44px;
    height: 44px;
    object-fit: cover;
    border-radius: 5px;
    flex: none;
    background: #18171a;
  }
  .ext {
    width: 44px;
    height: 44px;
    flex: none;
    border-radius: 5px;
    background: var(--raise);
    display: grid;
    place-items: center;
    font-family: var(--mono);
    font-size: 10.5px;
    font-weight: 600;
    color: var(--muted);
  }
  .meta { display: flex; flex-direction: column; min-width: 0; line-height: 1.3; }
  .name { font-size: 13px; color: var(--ink-2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .sub { font-size: 11.5px; color: var(--faint); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .failed .sub { color: var(--coral); }
  .act { font-size: 12px; font-weight: 600; color: var(--hue-1); flex: none; }
  .x {
    position: absolute;
    top: 50%;
    right: 2px;
    transform: translateY(-50%);
    width: 44px;
    height: 44px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    color: var(--muted);
    transition: color var(--motion-fast) var(--ease), background-color var(--motion-fast) var(--ease);
  }
  @media (hover: hover) {
    .x:hover { color: var(--ink); background: var(--hover); }
  }
  .x:active {
    background: var(--select);
}
</style>
