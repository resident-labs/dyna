<script lang="ts">
  import { balancesText, hideLabel, holdFor, outOfLimit, windowMeter, type UsageRow } from "../usage.ts";
  import Icon from "./Icon.svelte";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One model account on Home, in the machine cards' style: its label, the provider's name when that adds to it, then a
  // tile per usage window, then any prepaid balance as muted text. A spent window or an empty balance marks the
  // whole card, since the account takes no more work until then. The icon at its corner hides the card, or shows it
  // again from Hidden.
  let { row, now, onhide, onshow }: { row: UsageRow; now: number; onhide?: () => void; onshow?: () => void } = $props();
  let spent = $derived(outOfLimit(row, now));
  let hideText = $derived(hideLabel(holdFor(row, now)));
</script>

<section class="card" aria-label={row.title}>
  <h3><span class="name">{row.title}</span>
    {#if row.provider}<span class="provider">{row.provider}</span>{/if}
    {#if spent || onhide || onshow}<span class="aside">{#if spent}<StatusPill tone="warning" label="Limit reached" />{/if}
      {#if onhide}<button type="button" class="eye" title={hideText} aria-label={`${hideText}: ${row.title}`} onclick={onhide}><Icon name="eyeOff" size={14} /></button>
      {:else if onshow}<button type="button" class="eye" title="Show" aria-label={`Show ${row.title}`} onclick={onshow}><Icon name="eye" size={14} /></button>{/if}</span>{/if}</h3>
  {#if row.windows.length}<div class="tiles">{#each row.windows as w (w.name)}<MeterTile meter={windowMeter(w, now)} />{/each}</div>{/if}
  {#if row.balances.length}<p class="balance">{balancesText(row.balances)}</p>{/if}
</section>

<style>
  /* A long label wraps, in full, and the provider and the pill take the next line. */
  .card h3 { flex-wrap: wrap; align-items: baseline; gap: 0 8px; }
  .name { min-width: 0; overflow-wrap: anywhere; }
  .provider { color: var(--muted); font-size: 12.5px; font-weight: 400; }
  .balance { margin: 0; color: var(--muted); font-size: 12.5px; }
  .aside { display: inline-flex; align-items: center; gap: 6px; }
  /* The icon stays quiet until the card has the pointer. */
  .eye { all: unset; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 6px; color: var(--muted); opacity: .4; cursor: pointer; }
  .card:hover .eye, .eye:focus-visible { opacity: .8; }
  .card .eye:hover { background: var(--hover); color: var(--text); opacity: 1; }
  .eye:focus-visible { outline: 2px solid var(--focus); }
  @media (hover: none) { .eye { width: 40px; height: 40px; margin: -8px; } }
  /* One column grid for every card, so a card of one window keeps a tile of the same width and bars compare across
     cards. 150 px keeps a reset time readable; several model-specific windows wrap. */
  .card .tiles { grid-auto-flow: row; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); }
</style>
