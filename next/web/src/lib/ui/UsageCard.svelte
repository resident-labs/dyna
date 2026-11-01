<script lang="ts">
  import { balancesText, hideLabel, outOfLimit, windowMeter, type UsageRow } from "../usage.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One model account on Home, in the machine cards' style: its label, the provider's name when that adds to it, then a
  // tile per usage window, then any prepaid balance as muted text. A spent window or an empty balance marks the
  // whole card, since the account takes no more work until then.
  let { row, now, onhide }: { row: UsageRow; now: number; onhide?: () => void } = $props();
  let spent = $derived(outOfLimit(row, now));
</script>

<section class="card" aria-label={row.title}>
  <h3><span class="name">{row.title}</span>
    {#if row.provider}<span class="provider">{row.provider}</span>{/if}
    {#if spent}<span class="aside"><StatusPill tone="warning" label="Limit reached" /></span>{/if}</h3>
  {#if row.windows.length}<div class="tiles">{#each row.windows as w (w.name)}<MeterTile meter={windowMeter(w, now)} />{/each}</div>{/if}
  {#if row.balances.length}<p class="balance">{balancesText(row.balances)}</p>{/if}
  {#if onhide}<button type="button" class="hide" onclick={onhide}>{hideLabel(row, now)}</button>{/if}
</section>

<style>
  /* A long label wraps, in full, and the provider and the pill take the next line. */
  .card h3 { flex-wrap: wrap; align-items: baseline; gap: 0 8px; }
  .name { min-width: 0; overflow-wrap: anywhere; }
  .provider { color: var(--muted); font-size: 12.5px; font-weight: 400; }
  .balance { margin: 0; color: var(--muted); font-size: 12.5px; }
  .hide { all: unset; margin-top: 10px; color: var(--muted); font-size: 12.5px; cursor: pointer; }
  .hide:hover { color: var(--text); }
  .hide:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  /* One column grid for every card, so a card of one window keeps a tile of the same width and bars compare across
     cards. 150 px keeps a reset time readable; several model-specific windows wrap. */
  .card .tiles { grid-auto-flow: row; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); }
</style>
