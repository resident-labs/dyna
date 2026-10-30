<script lang="ts">
  import { RoomStore } from "./lib/room.svelte.ts";
  import { call } from "./lib/client.ts";
  import type { RoomSummary } from "./lib/contract.gen.ts";
  let rooms: RoomSummary[] = $state([]);
  void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }).catch(() => {});
  let path = $state(window.location.pathname);
  window.addEventListener("popstate", () => { path = window.location.pathname; });
  let room = $derived(/^\/r\/([^/]+)/.exec(path)?.[1]);
  let session = $derived(/^\/r\/[^/]+\/s\/([^/]+)/.exec(path)?.[1]);
  let unavailable = $state(false);
  $effect(() => {
    if (!room) return;
    const store = new RoomStore(decodeURIComponent(room));
    unavailable = false;
    void store.open().catch(() => { unavailable = true; });
    return () => store.close();
  });
</script>

<div class="shell">
  <aside aria-label="Rooms">
    <header><strong>Dyna</strong></header>
    <div class="section-title">Rooms</div>
    {#if rooms.length}
      <nav>{#each rooms as item (item.id)}
        <a href={"/r/" + encodeURIComponent(item.id)} aria-current={room === encodeURIComponent(item.id) ? "page" : undefined}>{item.name}</a>
      {/each}</nav>
    {:else}<p class="hint">Your rooms will appear here.</p>{/if}
  </aside>
  <main>
    <header><span>{room ? (session ? "Session" : rooms.find(item => encodeURIComponent(item.id) === room)?.name ?? "Room") : "Welcome"}</span></header>
    <section class="empty">
      <h1>{room ? (session ? "Session conversation" : "Room feed") : "Select a room"}</h1>
      <p>{unavailable ? "This room is not available." : room ? "Messages will appear here." : "Choose a room to see its conversation."}</p>
    </section>
  </main>
</div>
