<script lang="ts">
  import { limitReached, windowMeter, type UsageRow } from "../usage.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One model account on Home, in the machine cards' style: its label, the provider's name when that adds to it, then a
  // tile per usage window. A spent window marks the whole card, since the account takes no more work until it resets.
  let { row, now }: { row: UsageRow; now: number } = $props();
  let spent = $derived(row.windows.some(w => limitReached(w, now)));
</script>

<section class="card" aria-label={row.title}>
  <h3><span class="name">{row.title}</span>
    {#if row.provider}<span class="provider">{row.provider}</span>{/if}
    {#if spent}<span class="aside"><StatusPill tone="warning" label="Limit reached" /></span>{/if}</h3>
  <div class="tiles">{#each row.windows as w (w.name)}<MeterTile meter={windowMeter(w, now)} />{/each}</div>
</section>

<style>
  /* A long label wraps, in full, and the provider and the pill take the next line. */
  .card h3 { flex-wrap: wrap; align-items: baseline; gap: 0 8px; }
  .name { min-width: 0; overflow-wrap: anywhere; }
  .provider { color: var(--muted); font-size: 12.5px; font-weight: 400; }
  /* One column grid for every card, so a card of one window keeps a tile of the same width and bars compare across
     cards. 150 px keeps a reset time readable; several model-specific windows wrap. */
  .card .tiles { grid-auto-flow: row; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); }
</style>
