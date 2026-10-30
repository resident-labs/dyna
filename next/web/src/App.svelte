<script lang="ts">
  import { RoomStore } from "./lib/room.svelte.ts";
  import { call } from "./lib/client.ts";
  import type { RoomSummary } from "./lib/contract.gen.ts";
  let rooms: RoomSummary[] = $state([]);
  void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }).catch(() => {});
  let path = $state(window.location.pathname);
  window.addEventListener("popstate", () => { path = window.location.pathname; });
  const segment = (pattern: RegExp) => { const match = pattern.exec(path)?.[1]; return match && decodeURIComponent(match); };
  let room = $derived(segment(/^\/r\/([^/]+)/));
  let session = $derived(segment(/^\/r\/[^/]+\/s\/([^/]+)/));
  let store: RoomStore | undefined = $state();
  let unavailable = $state(false);
  $effect(() => {
    if (!room) return;
    const current = new RoomStore(room);
    store = current;
    unavailable = false;
    void current.open().catch(() => { if (store === current) unavailable = true; });
    return () => current.close();
  });
  function navigate(event: MouseEvent & { currentTarget: HTMLAnchorElement }) {
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey) return;
    event.preventDefault();
    history.pushState(null, "", event.currentTarget.href);
    path = window.location.pathname;
  }
  let sidebar: HTMLElement;
  let content: HTMLElement;
  // F6 (mod+F6 in a browser) moves focus between areas; Shift goes back.
  function cycle(event: KeyboardEvent) {
    if (event.key !== "F6") return;
    event.preventDefault();
    const areas = [sidebar, content];
    const index = areas.findIndex(area => area.contains(document.activeElement));
    areas[(index + (event.shiftKey ? areas.length - 1 : 1)) % areas.length].focus();
  }
  let title =$derived(!room ? "Welcome" : session ? store?.sessions.get(session)?.name ?? "Session"
    : rooms.find(item => item.id === room)?.name ?? "Room");
</script>

<svelte:window onkeydown={cycle} />
<div class="shell">
  <aside aria-label="Rooms" tabindex="-1" bind:this={sidebar}>
    <header><strong>Dyna</strong></header>
    <div class="section-title">Rooms</div>
    {#if rooms.length}
      <nav>{#each rooms as item (item.id)}
        <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}>{item.name}</a>
      {/each}</nav>
    {:else}<p class="hint">Your rooms will appear here.</p>{/if}
  </aside>
  <main tabindex="-1" bind:this={content}>
    <header><span>{title}</span></header>
    <section class="empty">
      <h1>{room ? (session ? "Session conversation" : "Room feed") : "Select a room"}</h1>
      <p>{unavailable ? "This room is not available." : room ? "Messages will appear here." : "Choose a room to see its conversation."}</p>
    </section>
  </main>
</div>
