<script lang="ts">
  import type { SubscriptionInfo } from "../contract.gen.ts";
  import { subscriptionRows, subscriptionSummary } from "../subscriptions.ts";
  import FoldRow from "./FoldRow.svelte";
  import StatusPill from "./StatusPill.svelte";
  // What the room is subscribed to, as one line that stays when it is closed, so a failure shows without opening anything.
  // Opened, each row is one line: a healthy hook says when it fires, and a disabled or running one says that instead. Only
  // a failure takes a second line. An argument gives way before the name, and the row's title keeps the raw command.
  let { subscriptions, name }: { subscriptions: SubscriptionInfo[]; name: (session: string) => string } = $props();
  let summary = $derived(subscriptionSummary(subscriptions));
  let rows = $derived(subscriptionRows(subscriptions, name));
</script>

<FoldRow more>
  {#snippet head()}{summary.count}{#if summary.health}, {#if summary.failing}<StatusPill tone="failed" word label={summary.health} />{:else}{summary.health}{/if}{/if}{/snippet}
  <ul>
    {#each rows as row (row.id)}
      <li class:off={row.disabled} title={row.title || undefined}>
        <span class="name"><span class="script">{row.name}</span>{#if row.detail}<span class="detail">{row.detail}</span>{/if}</span>
        <span class="when">{row.state && !row.failing ? row.state : row.when}</span>
        {#if row.failing}<span class="state"><StatusPill tone="failed" word label={row.state || "Failed"} /></span>{/if}
      </li>
    {/each}
  </ul>
</FoldRow>

<style>
  ul { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
  li { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: baseline; gap: 0 8px; line-height: 20px; }
  .name { display: flex; align-items: baseline; gap: 4px; min-width: 0; }
  .script, .detail { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .script { flex: 0 1 auto; }
  .detail { flex: 0 1000 auto; color: var(--muted); font-size: 12.5px; }
  .when { color: var(--muted); font-size: 12.5px; white-space: nowrap; }
  .state { grid-column: 1 / -1; }
  .off .script { color: var(--muted); }
</style>
