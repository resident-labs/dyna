<script>
  import { human, extOf } from './attach.js'
  let { items = [], onremove, onretry } = $props()
</script>

{#if items.length}
  <div class="tray" aria-label="Attachments">
    {#each items as it (it.key)}
      <div class="chip" class:failed={it.status === 'failed'} class:loading={it.status === 'uploading'}>
        {#if it.image && it.url}
          <img src={it.url} alt="" />
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

<style>
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
    padding: 8px 30px 8px 8px;
    background: var(--side);
    border: 1px solid var(--line-soft);
    border-radius: 8px;
  }
  .chip.failed { border-color: color-mix(in srgb, var(--coral) 45%, transparent); }
  .chip.loading { opacity: 0.7; }
  img {
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
    top: 6px;
    right: 6px;
    width: 20px;
    height: 20px;
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
