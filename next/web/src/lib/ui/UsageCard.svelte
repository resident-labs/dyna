<script lang="ts">
  import type { AccountProviderInfo, AccountUsage } from "../contract.gen.ts";
  import { causeText, providerName, visibleWindows, windowMeter } from "../usage.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One model account on Home, in the machine cards' style: the provider name (never the label, which can be an email), then a tile per usage window. A cause
  // shows its pill and what to do; an account without windows says so.
  let { account, providers, now }: { account: AccountUsage; providers: AccountProviderInfo[]; now: number } = $props();
  let windows = $derived(visibleWindows(account.windows ?? []));
</script>

<section class="card" aria-label={providerName(account.provider, providers)}>
  <h3>{providerName(account.provider, providers)}</h3>
  {#if account.cause}{#if causeText[account.cause].tone}<p><StatusPill tone="warning" label={causeText[account.cause].pill} /></p>{:else}<p class="aside">{causeText[account.cause].pill}</p>{/if}
    <p class="aside">{causeText[account.cause].line}</p>
  {:else if windows.length}<div class="tiles">{#each windows as w (w.name)}<MeterTile meter={windowMeter(w, now)} />{/each}</div>
  {:else}<p class="aside">No usage limits reported.</p>{/if}
</section>

<style>
  /* Keep each reset time readable when a card has several model-specific windows. */
  @container (max-width: 400px) {
    .card .tiles { grid-auto-flow: row; grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .tiles :global(.tile:only-child) { grid-column: 1 / -1; }
  }
</style>
