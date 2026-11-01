<script lang="ts">
  // A read's image on its row. An image that does not load leaves the row as it reads without one.
  let { src, name, onopen }: { src: string; name: string; onopen: () => void } = $props();
  let failed = $state(false);
</script>

{#if !failed}<button type="button" class="thumb" aria-label={`Open ${name}`} title={name} onclick={event => { event.preventDefault(); event.stopPropagation(); onopen(); }}><img {src} alt="" loading="lazy" onerror={() => failed = true} /></button>{/if}

<style>
  .thumb { flex: none; display: block; width: 32px; height: 22px; padding: 0; border: 0; border-radius: 4px; overflow: hidden; background: var(--sidebar); box-shadow: 0 0 0 1px var(--line); cursor: pointer; }
  .thumb:hover { box-shadow: 0 0 0 1px var(--firm); }
  .thumb:focus-visible { outline: 2px solid var(--focus); outline-offset: 1px; }
  img { display: block; width: 100%; height: 100%; object-fit: cover; }
</style>
