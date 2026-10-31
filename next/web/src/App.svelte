<script lang="ts">
  import { tick } from "svelte";
  import { RoomStore } from "./lib/room.svelte.ts";
  import { APIError, call } from "./lib/client.ts";
  import Feed from "./lib/ui/Feed.svelte";
  import StatusPill from "./lib/ui/StatusPill.svelte";
  import Composer from "./lib/ui/Composer.svelte";
  import SessionRow from "./lib/ui/SessionRow.svelte";
  import WorkingDot from "./lib/ui/WorkingDot.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import Avatar from "./lib/ui/Avatar.svelte";
  import RoomIntro from "./lib/ui/RoomIntro.svelte";
  import DirectMessages from "./lib/ui/DirectMessages.svelte";
  import ComputerView from "./lib/ui/ComputerView.svelte";
  import { MediaQuery } from "svelte/reactivity";
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
  // The sessions panel lists each child under its parent. Closed sessions leave it.
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
    if (event.key === "Escape" && panel) void showPanel(false);
    if (event.key !== "F6") return;
    event.preventDefault();
    // Only areas on screen take part; below 900 px the closed sessions panel has no boxes.
    const areas = [rail, feed, composer, members].filter(area => area !== undefined)
      .filter(area => area.getClientRects().length > 0);
    const index = areas.findIndex(area => area.contains(document.activeElement));
    areas[(index + (event.shiftKey ? areas.length - 1 : 1)) % areas.length].focus();
  }
  // Up and Down move between the sessions in the tree; Enter opens one.
  function walkTree(event: KeyboardEvent & { currentTarget: HTMLAnchorElement }) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" || !members) return;
    event.preventDefault();
    const links = Array.from(members.querySelectorAll<HTMLAnchorElement>("a.member"));
    const index = links.indexOf(event.currentTarget);
    links[Math.max(0, Math.min(links.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)))]?.focus();
  }
  let roomName = $derived(rooms.find(item => item.id === room)?.name ?? "Room");
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
  let counts = $derived(store?.ready ? plural(store.sessions.size, "session") + (store.turns.size ? ` · ${store.turns.size} working` : "") : "");
  // A session's muted status line: what it does now, then its profile.
  const status = (id: string, profile: string) => {
    const box = store?.boxes.get(id);
    return `${store?.turns.has(id) ? "working" : box && box !== "ready" ? `box ${box}` : "idle"} · ${profile}`;
  };
  const sessionHref = (id: string) => `/r/${encodeURIComponent(room ?? "")}/s/${encodeURIComponent(id)}`;
  // Below 900 px the sessions panel is hidden; a header button opens it over the feed.
  // Opening moves focus into the panel; closing returns it to the button if the panel held it.
  let panel = $state(false);
  // The session whose computer the sessions panel shows instead of the list, while it runs. The view
  // mounts only where the panel is on screen: always when wide, below 900 px only while it is open.
  let watching: string | undefined = $state();
  // The view closes when its computer stops or the room changes, before the DOM updates, so it never
  // streams again unasked. Focus that was in the view returns to the row's button, or to the panel.
  async function stopWatching() {
    const id = watching ?? "";
    const held = members?.contains(document.activeElement);
    watching = undefined;
    await tick();
    if (held) (members?.querySelector<HTMLElement>(`[data-watch="${CSS.escape(id)}"]`) ?? members)?.focus();
  }
  $effect.pre(() => { if (watching && !store?.computers.has(watching)) void stopWatching(); });
  const wide = new MediaQuery("min-width: 901px");
  let toggle: HTMLButtonElement | undefined = $state();
  async function showPanel(open: boolean) {
    const held = members?.contains(document.activeElement);
    panel = open;
    if (open) { await tick(); members?.focus(); } else if (held) toggle?.focus();
  }
  let title = $derived(!room ? "Welcome" : session ? store?.names.get(session) ?? "Session" : roomName);
  // The session page header: muted facts, and one pill only for a failed turn or a failed or lost box.
  let facts = $derived.by(() => {
    if (!session || !store) return [];
    const open = store.sessions.get(session);
    if (!open) return store.ready ? ["closed"] : [];
    // A failed or lost box shows as the pill, never twice.
    const box = store.boxes.get(session);
    return [open.profile, box && box !== "failed" && box !== "lost" && `box ${box}`].filter(fact => typeof fact === "string");
  });
  const trouble = (id: string) => {
    const box = store?.boxes.get(id);
    // A running turn replaces a failed one, as the working dot does in the tree.
    return store?.failed.has(id) && !store.turns.has(id) ? "Last turn failed" : box === "failed" ? "Box failed" : box === "lost" ? "Box lost" : "";
  };
</script>

{#snippet intro()}
  {#if session}<RoomIntro kind="session" name={title} root={!!store?.sessions.get(session) && !store.sessions.get(session)?.parent} line={`Your direct messages with ${title}`} />
  {:else}<RoomIntro kind="room" name={roomName} line={`The start of ${roomName}${store ? ` · ${plural(store.sessions.size, "session")}` : ""}`} />{/if}
{/snippet}

<svelte:window onkeydown={cycle} />
<div class="shell" class:open={room}>
  <aside class="rooms" aria-label="Rooms" tabindex="-1" bind:this={rail}>
    <nav class="rail" aria-label="Rooms">{#each rooms as item (item.id)}
      <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}
        aria-label={item.name} title={item.name}><Avatar kind="room" name={item.name} size={36} /></a>
    {/each}</nav>
    <div class="column">
      <header><strong>{room ? roomName : "Dyna"}</strong></header>
      {#if room}
        <nav aria-label="Room"><a href={"/r/" + encodeURIComponent(room)} onclick={navigate} aria-current={session ? undefined : "page"}><Icon name="room" />feed</a></nav>
        {#if store && !unavailable}<DirectMessages {store} href={sessionHref} current={session} {navigate} />{/if}
      {:else}<p class="hint">{roomsError || (rooms.length ? "Select a room." : "Your rooms will appear here.")}</p>{/if}
      <footer><Avatar kind="owner" name="You" size={28} /><strong>You</strong></footer>
    </div>
  </aside>
  <main>
    <header>{#if room && !session}<Icon name="room" />{/if}<strong>{title}</strong>
      {#if !session && counts}<span class="count">{counts}</span>{/if}
      {#if facts.length}<span class="count">· {facts.join(" · ")}</span>{/if}
      {#if session && trouble(session)}<StatusPill tone="failed">{trouble(session)}</StatusPill>{/if}
      {#if room}<button type="button" class="panel-toggle" bind:this={toggle} aria-label={store?.failed.size ? "Sessions, a turn failed" : "Sessions"}
        title="Sessions" aria-controls="sessions" aria-expanded={panel} onclick={() => void showPanel(!panel)}><Icon name="people" />
        {#if store?.failed.size && !panel}<span class="attention"></span>{/if}</button>{/if}</header>
    <div class="feed">
      {#if store && !unavailable}
        {#key key}<Feed bind:this={view} {store} {home} bind:element={feed} {intro} />{/key}
      {:else}<p class="empty">{unavailable || "Select a room."}
        {#if store && unavailable && !signedOut(store.error)}<button type="button" class="retry" onclick={() => void store?.open()}>Try again</button>{/if}</p>{/if}
    </div>
    {#if room && !unavailable}
      <Composer bind:element={composer} bind:value={() => drafts[key] ?? "", text => { drafts[key] = text; }} onsend={send} placeholder={"Message " + title} />
      {#if failed === key}<p class="hint" role="alert">Your message was not sent. Press Enter to try again.</p>{/if}
    {/if}
  </main>
  {#if room}
    <aside class="members" id="sessions" class:shown={panel} class:watching={watching !== undefined} aria-label="Sessions" tabindex="-1" bind:this={members}>
      {#if watching}
        {#if wide.current || panel}<ComputerView session={watching} name={store?.names.get(watching) ?? "Session"} onback={() => void stopWatching()} />{/if}
      {:else}
      <header class="label">Sessions</header>
      {#each tree as { session: item, depth } (item.session)}
        <div class="row"><a class="member" href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(item.session)}`} onclick={navigate} onkeydown={walkTree}
          aria-current={session === item.session ? "page" : undefined} style:padding-left="{16 + depth * 14}px">
          <SessionRow name={item.name} root={!item.parent} status={status(item.session, item.profile)} />
          {#if store?.turns.has(item.session)}<WorkingDot label="Working" />
          {:else if trouble(item.session)}<StatusPill tone="failed">{store?.failed.has(item.session) ? "Failed" : trouble(item.session)}</StatusPill>{/if}</a>
          {#if store?.computers.has(item.session)}<button type="button" class="watch" data-watch={item.session} aria-label={`Watch ${item.name}'s computer`}
            title={`Watch ${item.name}'s computer`} onclick={() => { watching = item.session; }}><Icon name="monitor" /></button>{/if}</div>
      {:else}<p class="hint">No sessions yet.</p>{/each}
      {/if}
    </aside>
  {/if}
</div>

<style>
  a.member { color: var(--text); text-decoration: none; }
  a.member[aria-current="page"] { background: var(--selected); }
  .row { display: flex; align-items: center; }
  .row a.member { flex: 1; min-width: 0; }
  .watch { all: unset; display: grid; place-items: center; width: 24px; height: 24px; margin-right: 8px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .watch:hover { background: var(--hover); color: var(--text); }
  .watch:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* The computer needs room: the panel widens while it shows one. */
  @media (min-width: 901px) { .shell.open:has(aside.watching) { grid-template-columns: 280px 1fr 420px; } }
  @media (max-width: 900px) { .shell > aside.members.shown.watching { width: min(420px, 100vw); } }
</style>
