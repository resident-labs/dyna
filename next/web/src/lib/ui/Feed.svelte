<script lang="ts">
  import { SvelteSet } from "svelte/reactivity";
  import VirtualList from "./VirtualList.svelte";
  import PostRow from "./PostRow.svelte";
  import FailedTurnRow from "./FailedTurnRow.svelte";
  import WorkRow from "./WorkRow.svelte";
  import { feedRows, type Row } from "../feed.ts";
  import type { RoomStore } from "../room.svelte.ts";
  import type { Conversation, Principal } from "../contract.gen.ts";

  let { store, home, empty, element = $bindable() }: {
    store: RoomStore; home: Conversation; empty: string; element?: HTMLDivElement;
  } = $props();

  type Item = Row | { kind: "pending"; key: string; text: string };
  let items: Item[] = $derived([
    ...feedRows(store.ordered, home),
    ...[...store.pendingPosts].filter(post => post.home.session === home.session)
      .map((post, index) => ({ kind: "pending" as const, key: `n${index}`, text: post.text })),
  ]);
  // Opened work rows stay open while the list recycles their elements.
  const opened = new SvelteSet<string>();
  const name = (session: string) => store.names.get(session) ?? "A closed session";
  const author = (from: Principal) => from.kind === "owner" ? "You" : from.session ? name(from.session) : "Dyna";
  const toggle = (key: string) => { if (!opened.delete(key)) opened.add(key); };
</script>

{#if items.length}
  <VirtualList {items} key={item => item.key} label="Feed" follow={store.atPresent} bind:element
    onstart={() => void store.loadOlder()} onend={() => void store.loadNewer()}>
    {#snippet row(item: Item)}
      {#if item.kind === "post"}<PostRow row={item} author={author(item.from)} />
      {:else if item.kind === "failed"}<FailedTurnRow row={item} author={name(item.session)} />
      {:else if item.kind === "work"}<WorkRow row={item} {name} open={opened.has(item.key)} ontoggle={() => toggle(item.key)} />
      {:else}<p class="pending">{item.text}</p>{/if}
    {/snippet}
  </VirtualList>
{:else if store.ready}
  <p class="empty">{empty}</p>
{/if}
{#if !store.atPresent}
  <button type="button" class="jump" onclick={() => void store.jumpToPresent()}>New posts below. Jump to present</button>
{/if}

<style>
  .pending { margin: 0; padding: 1px 16px; color: var(--muted); white-space: pre-wrap; overflow-wrap: anywhere; }
  .jump { all: unset; cursor: pointer; padding: 6px 16px; color: var(--muted); font-size: 12px; }
  .jump:hover { color: var(--text); }
  .jump:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
</style>
