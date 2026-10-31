<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";
  import RequestCard from "./RequestCard.svelte";
  import type { RoomStore } from "../room.svelte.ts";
  let { store, room, onback, element = $bindable() }: { store: RoomStore; room: string; onback: () => void; element?: HTMLElement } = $props();
  // Waiting requests first, in the order they came: the owner's own, which Home and the room badge count, then
  // the ones a session above the asker may also decide. Then the ones decided since the room opened, newest first.
  let requests = $derived([...store.requests.values()]);
  let waiting = $derived(requests.filter(request => request.state === "pending" && request.class === "owner"));
  let shared = $derived(requests.filter(request => request.state === "pending" && request.class !== "owner"));
  let decided = $derived(requests.filter(request => request.state !== "pending")
    .sort((a, b) => (b.decision?.at ?? "").localeCompare(a.decision?.at ?? "")));
  // Relative times refresh each half minute while the panel is open; this reads nothing from the engine.
  let now = $state(Date.now());
  onMount(() => {
    // Focus moves to the panel; its ring shows only after keyboard use (:focus-visible).
    element?.focus();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });
</script>

<section class="panel" aria-label="Requests" tabindex="-1" bind:this={element}>
  <div class="top"><button type="button" class="back" onclick={onback}><Icon name="back" />Back</button>
    <span class="room">{room}</span><strong>Requests</strong></div>
  <div class="list">
    <h2 class="label">Waiting for you</h2>
    {#each waiting as request (request.id)}<RequestCard {request} {store} {now} />
    {:else}{#if store.ready}<p class="none">No requests are waiting.</p>{/if}{/each}
    {#if shared.length}
      <h2 class="label">Waiting for a session</h2>
      {#each shared as request (request.id)}<RequestCard {request} {store} {now} />{/each}
    {/if}
    {#if decided.length}
      <h2 class="label">Decided</h2>
      {#each decided as request (request.id)}<RequestCard {request} {store} {now} />{/each}
    {/if}
  </div>
</section>

<style>
  .panel { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  .top { display: flex; align-items: center; gap: 8px; padding: 10px 16px; border-bottom: 1px solid var(--line); }
  .back { display: flex; align-items: center; gap: 6px; padding: 4px 8px 4px 4px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; cursor: pointer; }
  .back:hover { background: var(--hover); color: var(--text); }
  .room { color: var(--muted); font-size: 13px; }
  .room::after { content: " /"; }
  .list { flex: 1; min-height: 0; overflow-y: auto; }
  h2 { margin: 0; padding: 16px 16px 4px; }
  .none { margin: 0; padding: 8px 16px; color: var(--muted); font-size: 13px; }
</style>
