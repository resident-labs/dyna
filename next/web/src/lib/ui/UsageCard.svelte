<script lang="ts">
  import type { AccountUsage } from "../contract.gen.ts";
  import { causeText, providerName, windowMeter } from "../usage.ts";
  import MeterTile from "./MeterTile.svelte";
  import StatusPill from "./StatusPill.svelte";
  // One model account on Home, in the machine cards' style: the provider name (never the label, which can be an email), then a tile per usage window. A cause
  // shows the warning pill and what to do; an account without windows says so.
  let { account, now }: { account: AccountUsage; now: number } = $props();
  let windows = $derived(account.windows ?? []);
</script>

<section class="card" aria-label={providerName(account.provider)}>
  <h3>{providerName(account.provider)}</h3>
  {#if account.cause}<p><StatusPill tone="warning" label={causeText[account.cause].pill} /></p>
    <p class="aside">{causeText[account.cause].line}</p>
  {:else if windows.length}<div class="tiles">{#each windows as w (w.name)}<MeterTile meter={windowMeter(w, now)} />{/each}</div>
  {:else}<p class="aside">No usage limits reported.</p>{/if}
</section>
