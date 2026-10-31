<script lang="ts">
  import { tick } from "svelte";
  import { RoomStore } from "./lib/room.svelte.ts";
  import { APIError, call } from "./lib/client.ts";
  import Feed from "./lib/ui/Feed.svelte";
  import StatusPill from "./lib/ui/StatusPill.svelte";
  import Composer from "./lib/ui/Composer.svelte";
  import SessionRow from "./lib/ui/SessionRow.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import Avatar from "./lib/ui/Avatar.svelte";
  import RoomIntro from "./lib/ui/RoomIntro.svelte";
  import { count } from "./lib/feed.ts";
  import ComputerView from "./lib/ui/ComputerView.svelte";
  import { MediaQuery, SvelteSet } from "svelte/reactivity";
  import RoomMenu, { type Section } from "./lib/ui/RoomMenu.svelte";
  import RequestsPanel from "./lib/ui/RequestsPanel.svelte";
  import type { Conversation, RoomSummary, SessionStarted } from "./lib/contract.gen.ts";
  const signedOut = (error: unknown) => error instanceof APIError && error.detail.code === "forbidden";
  let rooms: RoomSummary[] = $state([]);
  let roomsError = $state("");
  void call("room.list", {}).then(result => { rooms = result.rooms ?? []; }).catch((error: unknown) => {
    roomsError = signedOut(error) ? "Your sign-in ended. Run dyna web to sign in again." : "Rooms are not available.";
  });
  let path = $state(window.location.pathname);
  window.addEventListener("popstate", () => { path = window.location.pathname; section = undefined; });
  // A room view from the header menu shows in place of the conversation until Back or Escape.
  let section: Section | undefined = $state();
  let menu: RoomMenu | undefined = $state();
  async function closeSection() {
    section = undefined;
    await tick();
    menu?.focus();
  }
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
    section = undefined;
  }
  let home: Conversation = $derived(session ? { kind: "dm", session } : { kind: "feed" });
  // The sessions panel lists each child under its parent. Closed sessions leave it. As in v1, a session with
  // children folds its subtree, and its row counts the hidden sessions; folds last for this page view.
  const folded = new SvelteSet<string>();
  let tree = $derived.by(() => {
    const sessions = [...(store?.sessions.values() ?? [])];
    const ids = new Set(sessions.map(item => item.session));
    const children = (parent: string | undefined) => sessions.filter(item => (item.parent && ids.has(item.parent) ? item.parent : undefined) === parent);
    const under = (parent: string): string[] => children(parent).flatMap(item => [item.session, ...under(item.session)]);
    const rows: { session: SessionStarted; depth: number; parent: boolean; hidden: number; hiddenTrouble: boolean }[] = [];
    const walk = (parent: string | undefined, depth: number) => {
      for (const item of children(parent)) {
        const hidden = folded.has(item.session) ? under(item.session) : [];
        // A folded row keeps the failure of a session it hides in sight, with the same rule as the row pill.
        rows.push({ session: item, depth, parent: children(item.session).length > 0, hidden: hidden.length, hiddenTrouble: hidden.some(id => trouble(id)) });
        if (!hidden.length) walk(item.session, depth + 1);
      }
    };
    walk(undefined, 0);
    return rows;
  });
  // Left folds a parent's subtree and Right opens it, on its row or its chevron.
  function fold(event: KeyboardEvent, id: string) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    if (event.key === "ArrowLeft") folded.add(id); else folded.delete(id);
  }
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
  let requests: HTMLElement | undefined = $state();
  let composer: HTMLTextAreaElement | undefined = $state();
  let members: HTMLElement | undefined = $state();
  // F6 (mod+F6 in a browser) moves focus between areas; Shift goes back.
  function cycle(event: KeyboardEvent) {
    if (event.key === "Escape" && panel) void showPanel(false);
    else if (event.key === "Escape" && section) void closeSection();
    if (event.key !== "F6") return;
    event.preventDefault();
    // Only areas on screen take part; below 900 px the closed sessions panel has no boxes.
    // A panel from the room menu takes the place of the feed and the composer.
    const areas = [rail, section ? requests : feed, section ? undefined : composer, members].filter(area => area != null)
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
  // A session's muted status line shows only state that matters: a running turn with its latest activity, or a
  // starting box. An idle session has none; its profile is the row's hover title.
  const status = (id: string) => {
    const box = store?.boxes.get(id);
    if (store?.turns.has(id)) { const label = store.activity.get(id); return label ? `working · ${label}` : "working"; }
    return box === "pending" || box === "creating" || box === "starting" ? "starting" : "";
  };
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
  // The open room's icon gets a dot while a request waits for the owner; other rooms need cross-room counts first.
  let waiting = $derived((store?.waiting ?? 0) > 0);
  // Each fact shows once: profile, state and trouble live on the session's row. A closed session has no row.
  let closed = $derived(!!session && !!store?.ready && !store.sessions.has(session));
  const trouble = (id: string) => {
    const box = store?.boxes.get(id);
    // A failed turn shows until the session's next turn succeeds.
    return store?.failed.has(id) ? "Last turn failed" : box === "failed" ? "Could not start" : box === "lost" ? "Stopped unexpectedly" : "";
  };
</script>

{#snippet intro()}
  {#if session}<RoomIntro kind="session" name={title} root={!!store?.sessions.get(session) && !store.sessions.get(session)?.parent} line={`This is the start of your direct messages with ${title}.`} />
  {:else}<RoomIntro kind="room" name={roomName} line={`This is the start of #${roomName}.`} />{/if}
{/snippet}

<svelte:window onkeydown={cycle} />
<div class="shell" class:open={room}>
  <aside class="rail" aria-label="Rooms" tabindex="-1" bind:this={rail}>
    <nav aria-label="Rooms">{#each rooms as item (item.id)}
      <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}
        aria-label={room === item.id && waiting ? `${item.name}, requests waiting` : item.name} title={item.name}>
        <Avatar kind="room" name={item.name} size={40} />{#if room === item.id && waiting}<span class="waiting"></span>{/if}</a>
    {/each}</nav>
    <footer><Avatar kind="owner" name="You" size={32} /></footer>
  </aside>
  <main>
    <header>{#if room && session}<a class="crumb" href={"/r/" + encodeURIComponent(room)} onclick={navigate}>{roomName}</a><span class="count">/</span>
      {:else if room}<Icon name="room" />{/if}<strong>{title}</strong>
      {#if closed}<span class="count">· closed</span>{/if}
      {#if store && !unavailable}<RoomMenu bind:this={menu} waiting={store.waiting} onchoose={next => { section = next; }} />{/if}
      {#if room}<button type="button" class="panel-toggle" bind:this={toggle} aria-label={store?.failed.size ? "Sessions, a turn failed" : "Sessions"}
        title="Sessions" aria-controls="sessions" aria-expanded={panel} onclick={() => void showPanel(!panel)}><Icon name="people" />
        {#if store?.failed.size && !panel}<span class="attention"></span>{/if}</button>{/if}</header>
    {#if section === "requests" && store && !unavailable}<RequestsPanel bind:element={requests} {store} room={roomName} onback={() => void closeSection()} />
    {:else}<div class="feed">
      {#if store && !unavailable}
        {#key key}<Feed bind:this={view} {store} {home} bind:element={feed} {intro} />{/key}
      {:else}<p class="empty">{unavailable || roomsError || (rooms.length ? "Select a room." : "Your rooms will appear here.")}
        {#if store && unavailable && !signedOut(store.error)}<button type="button" class="retry" onclick={() => void store?.open()}>Try again</button>{/if}</p>{/if}
    </div>
    {#if room && !unavailable}
      <Composer bind:element={composer} bind:value={() => drafts[key] ?? "", text => { drafts[key] = text; }} onsend={send} placeholder={"Message " + title} />
      {#if failed === key}<p class="hint" role="alert">Your message was not sent. Press Enter to try again.</p>{/if}
    {/if}{/if}
  </main>
  {#if room}
    <aside class="members" id="sessions" class:shown={panel} class:watching={watching !== undefined} aria-label="Sessions" tabindex="-1" bind:this={members}>
      {#if watching}
        {#if wide.current || panel}<ComputerView session={watching} name={store?.names.get(watching) ?? "Session"} onback={() => void stopWatching()} />{/if}
      {:else}
      <header class="label">Sessions</header>
      {#each tree as { session: item, depth, parent, hidden, hiddenTrouble } (item.session)}
        <div class="row">{#if parent}<button type="button" class="fold" aria-expanded={!hidden} style:left="{depth * 14}px"
          aria-label={hidden ? `Show ${count(hidden, "session")} under ${item.name}` : `Hide the sessions under ${item.name}`}
          title={hidden ? `${count(hidden, "session")} folded` : "Fold"} onclick={() => { if (!folded.delete(item.session)) folded.add(item.session); }}
          onkeydown={event => fold(event, item.session)}><Icon name="chevron" size={12} />{#if hidden}<span>{hidden}</span>{/if}</button>{/if}
        <a class="member" href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(item.session)}`} onclick={navigate}
          onkeydown={event => { walkTree(event); if (parent) fold(event, item.session); }}
          aria-current={session === item.session ? "page" : undefined} title={item.profile} style:padding-left="{16 + depth * 14}px">
          <SessionRow name={item.name} root={!item.parent} status={status(item.session)} />
          {#if trouble(item.session)}<StatusPill tone="failed">{store?.failed.has(item.session) ? "Failed" : trouble(item.session)}</StatusPill>
          {:else if hiddenTrouble}<StatusPill tone="failed">Failed</StatusPill>{/if}</a>
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
  .row { position: relative; display: flex; align-items: center; }
  /* The chevron sits in the gutter left of the avatar; a folded row stacks its hidden count under it, as in v1. */
  .fold { all: unset; position: absolute; z-index: 1; top: 50%; transform: translateY(-50%); width: 16px; min-height: 24px; display: grid; justify-items: center; align-content: center;
    padding: 2px 0; border-radius: 4px; color: var(--muted); font-size: 10px; line-height: 1.1; font-variant-numeric: tabular-nums; cursor: pointer; }
  .fold:hover { background: var(--hover); color: var(--text); }
  .fold:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .fold[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  /* A thumb gets v1's taller target, so a near miss does not open the DM. */
  @media (hover: none) { .fold { width: 24px; min-height: 44px; } }
  .row a.member { flex: 1; min-width: 0; }
  .watch { all: unset; display: grid; place-items: center; width: 24px; height: 24px; margin-right: 8px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .watch:hover { background: var(--hover); color: var(--text); }
  .watch:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* The computer needs room: the panel widens while it shows one. */
  @media (min-width: 901px) { .shell.open:has(aside.watching) { grid-template-columns: 64px 1fr 420px; } }
  @media (max-width: 900px) { .shell > aside.members.shown.watching { width: min(420px, 100vw); } }
</style>
