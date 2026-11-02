<script lang="ts">
  import type { SubscriptionInfo } from "../contract.gen.ts";
  import { subscriptionRows } from "../subscriptions.ts";
  import StatusPill from "./StatusPill.svelte";
  // What the room is subscribed to, in About's Hooks modal: failures first, then hooks, wakes, and disabled hooks last.
  // Each row says it all and wraps: the hook's name and arguments, every event that fires it, and its whole command. A
  // healthy hook shows no state; a failing, running or disabled one says so.
  let { subscriptions, name }: { subscriptions: SubscriptionInfo[]; name: (session: string) => string } = $props();
  let rows = $derived(subscriptionRows(subscriptions, name));
</script>

<ul>
  {#each rows as row (row.id)}
    <li class:off={row.disabled}>
      <p class="head"><strong>{row.name}</strong>{#if row.args.length}<span class="args">{row.args.join(" ")}</span>{/if}
        {#if row.failing}<StatusPill tone="failed" word label={row.state || "Failed"} />{:else if row.state}<span class="state">{row.state}</span>{/if}</p>
      <p class="when">{row.fires}</p>
      {#if row.command}<code>{row.command}</code>{/if}
    </li>
  {/each}
</ul>

<style>
  ul { display: grid; margin: 0; padding: 0; list-style: none; }
  li { display: grid; gap: 2px; min-width: 0; padding: 10px 0; border-top: 1px solid var(--line); }
  li:first-child { padding-top: 0; border-top: 0; }
  li:last-child { padding-bottom: 0; }
  p { margin: 0; min-width: 0; overflow-wrap: anywhere; }
  .head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; line-height: 20px; }
  .args { color: var(--muted); font: 12.5px var(--mono); }
  .state, .when { color: var(--muted); font-size: 12.5px; }
  .off strong { color: var(--muted); font-weight: 500; }
  code { color: var(--muted); font: 12.5px/1.45 var(--mono); overflow-wrap: anywhere; }
</style>
