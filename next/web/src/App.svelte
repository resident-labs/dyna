<script lang="ts">
  import { RoomStore } from "./lib/room.svelte.ts";
  import { APIError, call } from "./lib/client.ts";
  import Feed from "./lib/ui/Feed.svelte";
  import StatusPill from "./lib/ui/StatusPill.svelte";
  import Composer from "./lib/ui/Composer.svelte";
  import SessionRow from "./lib/ui/SessionRow.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import type { Conversation, RoomSummary, SessionStarted } from "./lib/contract.gen.ts";
  const signedOut = (error: unknown) => error instanceof APIError && error.detail.code === "forbidden";
  let rooms: RoomSummary[] = $state([]);
  let roomsError = $state("");
  void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }).catch((error: unknown) => {
    roomsError = signedOut(error) ? "Your sign-in ended. Run dyna web to sign in again." : "Rooms are not available.";
  });
  let path = $state(window.location.pathname);
  window.addEventListener("popstate", () => { path = window.location.pathname; });
  const segment = (pattern: RegExp) => { const match = pattern.exec(path)?.[1]; return match && decodeURIComponent(match); };
  let room = $derived(segment(/^\/r\/([^/]+)/));
  let session = $derived(segment(/^\/r\/[^/]+\/s\/([^/]+)/));
  let store: RoomStore | undefined = $state();
  $effect(() => {
    if (!room) return;
    const current = new RoomStore(room);
    store = current;
    void current.open();
    return () => current.close();
  });
  // A room that failed to open is unavailable; later load errors show inside the feed.
  let unavailable = $derived(!store || store.ready || store.error === undefined ? ""
    : signedOut(store.error) ? "Your sign-in ended. Run dyna web to sign in again." : "This room is not available.");
  function navigate(event: MouseEvent & { currentTarget: HTMLAnchorElement }) {
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey) return;
    event.preventDefault();
    history.pushState(null, "", event.currentTarget.href);
    path = window.location.pathname;
  }
  let home: Conversation = $derived(session ? { kind: "dm", session } : { kind: "feed" });
  // The sessions panel lists each child under its parent; PR 5 makes the full tree.
  let tree = $derived.by(() => {
    const sessions = [...(store?.sessions.values() ?? [])];
    const ids = new Set(sessions.map(item => item.session));
    const rows: { session: SessionStarted; depth: number }[] = [];
    const walk = (parent: string | undefined, depth: number) => {
      for (const item of sessions) {
        if ((item.parent && ids.has(item.parent) ? item.parent : undefined) !== parent) continue;
        rows.push({ session: item, depth });
        walk(item.session, depth + 1);
      }
    };
    walk(undefined, 0);
    return rows;
  });
  // Drafts and send errors belong to one conversation, keyed by room and session.
  let drafts: Record<string, string> = $state({});
  let failed: string | undefined = $state();
  let key = $derived(`${room}/${session ?? ""}`);
  function send() {
    if (!store || !room) return;
    const sent = key;
    const text = drafts[sent] ?? "";
    if (!text.trim()) return;
    drafts[sent] = "";
    failed = undefined;
    view?.send({ room, text, home: session ? home : undefined }).catch(() => { drafts[sent] = text; failed = sent; });
  }
  let view: Feed | undefined = $state();
  let rail: HTMLElement;
  let feed: HTMLDivElement | undefined = $state();
  let composer: HTMLTextAreaElement | undefined = $state();
  let members: HTMLElement | undefined = $state();
  // F6 (mod+F6 in a browser) moves focus between areas; Shift goes back.
  function cycle(event: KeyboardEvent) {
    if (event.key !== "F6") return;
    event.preventDefault();
    const areas = [rail, feed, composer, members].filter(area => area !== undefined);
    const index = areas.findIndex(area => area.contains(document.activeElement));
    areas[(index + (event.shiftKey ? areas.length - 1 : 1)) % areas.length].focus();
  }
  let roomName = $derived(rooms.find(item => item.id === room)?.name ?? "Room");
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
  let counts = $derived(store?.ready ? `${plural(store.sessions.size, "session")} · ${store.turns.size} working` : "");
  // A session's muted status line: what it does now, then its profile.
  const status = (id: string, profile: string) => {
    const box = store?.boxes.get(id);
    return `${store?.turns.has(id) ? "working" : box && box !== "ready" ? `box ${box}` : "idle"} · ${profile}`;
  };
  let title = $derived(!room ? "Welcome" : session ? store?.sessions.get(session)?.name ?? "Session" : roomName);
</script>

<svelte:window onkeydown={cycle} />
<div class="shell" class:open={room}>
  <aside aria-label="Rooms" tabindex="-1" bind:this={rail}>
    <header><Icon name="root" /><strong>Dyna</strong></header>
    <div class="section-title">Rooms</div>
    {#if rooms.length}
      <nav>{#each rooms as item (item.id)}
        <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}><Icon name="room" />{item.name}</a>
      {/each}</nav>
    {:else}<p class="hint">{roomsError || "Your rooms will appear here."}</p>{/if}
  </aside>
  <main>
    <header>{#if room && !session}<Icon name="room" />{/if}<strong>{title}</strong>
      {#if !session && counts}<span class="count">{counts}</span>{/if}</header>
    <div class="feed">
      {#if store && !unavailable}
        {#key key}<Feed bind:this={view} {store} {home} bind:element={feed} empty={session ? "No messages yet." : "No posts yet."} />{/key}
      {:else}<p class="empty">{unavailable || "Select a room."}</p>{/if}
    </div>
    {#if room && !unavailable}
      <Composer bind:element={composer} bind:value={() => drafts[key] ?? "", text => { drafts[key] = text; }} onsend={send} placeholder={"Message " + title} />
      {#if failed === key}<p class="hint" role="alert">Your message was not sent. Press Enter to try again.</p>{/if}
    {/if}
  </main>
  {#if room}
    <aside class="members" aria-label="Sessions" tabindex="-1" bind:this={members}>
      <header class="label">Sessions</header>
      {#each tree as { session: item, depth } (item.session)}
        <div class="member" style:padding-left="{16 + depth * 14}px"><SessionRow name={item.name} status={status(item.session, item.profile)} />
          {#if store?.failed.has(item.session)}<StatusPill tone="failed">Failed</StatusPill>{/if}</div>
      {:else}<p class="hint">No sessions yet.</p>{/each}
    </aside>
  {/if}
</div>
