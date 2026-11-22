<script lang="ts">
  import type { Service, SubscriptionInfo } from "../contract.gen.ts";
  import { fullTime } from "../feed.ts";
  import { serviceRows } from "../services.ts";
  import StatusPill from "./StatusPill.svelte";
  // The room's declared services, in About's Services modal: failures first, then active, then stopped.
  // Each row shows state, host, last error, and for a feed: last message time, in/out counts, and routing subscriptions.
  let { services, subscriptions, name, now }: {
    services: Service[];
    subscriptions: SubscriptionInfo[];
    name: (session: string) => string;
    now: number;
  } = $props();
  let rows = $derived(serviceRows(services, subscriptions, name, now));
</script>

<ul>
  {#each rows as row (row.id)}
    <li class:off={row.stopped}>
      <p class="head"><strong>{row.name}</strong>{#if row.feed}<span class="tag">feed</span>{/if}{#if row.host}<span class="host">{row.host}</span>{/if}
        {#if row.failing}<StatusPill tone="failed" word label={row.state} />{:else}<span class="state">{row.state}</span>{/if}</p>
      {#if row.error}<p class="error" class:danger={row.failing}>{row.error}</p>{/if}
      {#if row.feed}
        <p class="stats">
          {#if row.lastMessageAt}Last message <time datetime={row.lastMessageAt} title={fullTime(row.lastMessageAt)}>{row.lastMessage}</time>{:else}{row.lastMessage}{/if}
          <span aria-hidden="true">·</span>
          <span class="counts">{row.messagesIn} in · {row.messagesOut} out</span>
        </p>
        {#if row.routes.length}
          <p class="routes">{#each row.routes as route, index (index)}<span class="route">{route}</span>{/each}</p>
        {:else}
          <p class="routes empty">No subscribers</p>
        {/if}
      {/if}
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
  .tag { padding: 0 6px; border: 1px solid var(--line); border-radius: 999px; color: var(--muted); font-size: 11.5px; line-height: 18px; white-space: nowrap; }
  .host { color: var(--muted); font: 12.5px var(--mono); }
  .state, .stats, .routes, .error { color: var(--muted); font-size: 12.5px; line-height: 1.45; }
  .stats { display: flex; flex-wrap: wrap; gap: 0 6px; font-variant-numeric: tabular-nums; }
  .routes { display: flex; flex-wrap: wrap; gap: 2px 10px; color: var(--text); }
  .routes.empty { color: var(--muted); }
  .error.danger { color: var(--danger); }
  .off strong { color: var(--muted); font-weight: 500; }
</style>
