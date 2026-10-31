<script lang="ts">
  import type { VisualCard } from "../contract.gen.ts";
  import { worstCheck } from "../visual.ts";
  let { visual, newest, onopen }: { visual: VisualCard; newest: VisualCard; onopen: () => void } = $props();
  let older = $derived(visual.version < newest.version);
  let fact = $derived(worstCheck(visual.checks));
  let failed = $state(false);
  $effect(() => { visual.banner; visual.thumbnail; failed = false; });
</script>

<button type="button" class="visual" class:older onclick={onopen} title={visual.title}>
  {#if older}
    {#if visual.thumbnail && !failed}<img class="thumb" src={"/v1/files/" + encodeURIComponent(visual.thumbnail)} alt="" loading="lazy" onerror={() => failed = true} />{/if}
    <span class="caption"><strong>{visual.title}</strong><span>v{visual.version} · Superseded by v{newest.version}</span></span>
  {:else}
    {#if visual.banner && !failed}<img class="banner" src={"/v1/files/" + encodeURIComponent(visual.banner)} alt="" loading="lazy" onerror={() => failed = true} />{/if}
    <span class="caption"><strong>{visual.title}</strong><span>v{visual.version}</span>{#if fact}<span>· {fact}</span>{/if}</span>
  {/if}
</button>

<style>
  .visual { display: block; width: min(560px, 100%); margin: 6px 0 2px; padding: 0; text-align: left; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); color: var(--text); font: inherit; overflow: hidden; cursor: pointer; }
  .visual:hover { background: var(--hover); }
  .banner { display: block; width: 100%; aspect-ratio: 8 / 3; object-fit: cover; object-position: top; }
  .caption { display: flex; min-width: 0; align-items: center; gap: 8px; padding: 8px 12px; white-space: nowrap; font-size: 12.5px; color: var(--muted); }
  .caption strong { flex: 0 1 auto; max-width: 65%; color: var(--text); overflow: hidden; text-overflow: ellipsis; min-width: 0; font-size: 14px; }
  .caption span { flex: none; }
  .caption span:last-child { min-width: 0; overflow: hidden; text-overflow: ellipsis; flex: 0 1 auto; }
  .older { display: inline-flex; width: auto; max-width: min(560px, 100%); align-items: center; border-radius: 6px; }
  .thumb { display: block; width: 64px; min-width: 64px; height: 36px; object-fit: cover; flex: none; }
  @media (hover: none) { .visual { min-height: 40px; } }
</style>
