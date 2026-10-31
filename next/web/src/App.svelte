<script lang="ts">
  import { tick } from "svelte";
  import { RoomStore } from "./lib/room.svelte.ts";
  import { APIError } from "./lib/client.ts";
  import Feed from "./lib/ui/Feed.svelte";
  import StatusPill from "./lib/ui/StatusPill.svelte";
  import Composer from "./lib/ui/Composer.svelte";
  import SessionRow from "./lib/ui/SessionRow.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import Avatar from "./lib/ui/Avatar.svelte";
  import RoomIntro from "./lib/ui/RoomIntro.svelte";
  import { count, quoted, type Quote } from "./lib/feed.ts";
  import ComputerView from "./lib/ui/ComputerView.svelte";
  import ShellView from "./lib/ui/ShellView.svelte";
  import TermDock from "./lib/ui/TermDock.svelte";
  import Home from "./lib/ui/Home.svelte";
  import { MediaQuery, SvelteSet } from "svelte/reactivity";
  import RoomMenu, { type Section } from "./lib/ui/RoomMenu.svelte";
  import RequestsPanel from "./lib/ui/RequestsPanel.svelte";
  import { Attention } from "./lib/attention.svelte.ts";
  import { mentionable, parseMentions } from "./lib/mentions.ts";
  import type { Conversation, PostRequest, SessionStarted } from "./lib/contract.gen.ts";
  const signedOut = (error: unknown) => error instanceof APIError && error.detail.code === "forbidden";
  const attention = new Attention();
  let rooms = $derived([...attention.rooms.values()]);
  let roomsError = $state("");
  void attention.open().catch((error: unknown) => {
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
    // Home has no room, so no room controls or panels may show one.
    if (!room) { store = undefined; return; }
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
  // Drafts and failed sends belong to one conversation, keyed by room and session. A failed send shows in its
  // feed with Retry, also after the owner leaves and comes back; raw keeps each request's identity for onsettled.
  let drafts: Record<string, string> = $state({});
  let unsent: Record<string, PostRequest[]> = $state.raw({});
  const keyOf = (request: PostRequest) => `${request.room}/${request.home?.session ?? ""}`;
  const keep = (request: PostRequest) => { unsent = { ...unsent, [keyOf(request)]: [...unsent[keyOf(request)] ?? [], request] }; };
  const settle = (request: PostRequest) => { unsent = { ...unsent, [keyOf(request)]: (unsent[keyOf(request)] ?? []).filter(kept => kept !== request) }; };
  // A forwarded post waits in its conversation's composer like a draft.
  let quotes: Record<string, Quote | undefined> = $state({});
  let key = $derived(`${room}/${session ?? ""}`);
  function send() {
    if (!store || !room) return;
    const text = drafts[key] ?? "", quote = quotes[key];
    if (!text.trim() && !quote) return;
    drafts[key] = "";
    quotes[key] = undefined;
    // The engine refuses mentions on a DM post; in the feed, @names of open sessions become mentions.
    // Mentions come from the owner's own words only, so an @name inside a forwarded post wakes nobody.
    const post = session ? { text, mentions: [] } : parseMentions(text, store.sessions.values());
    // A forward carries the post's files; the engine keeps each file once, by hash, for every conversation.
    void view?.send({ room, text: quote ? quoted(quote, post.text) : post.text, mentions: post.mentions.length ? post.mentions : undefined,
      home: session ? home : undefined, attachments: quote?.attachments.length ? quote.attachments : undefined });
  }
  let view: Feed | undefined = $state();
  let rail: HTMLElement;
  let feed: HTMLDivElement | undefined = $state();
  let requests: HTMLElement | undefined = $state();
  let composer: HTMLTextAreaElement | undefined = $state();
  let members: HTMLElement | undefined = $state();
  let computer: HTMLElement | undefined = $state();
  let terminals: TermDock | undefined = $state();
  let dock: HTMLElement | undefined = $state();
  // F6 (mod+F6 in a browser) moves focus between areas; Shift goes back.
  function cycle(event: KeyboardEvent) {
    // Escape closes the jobs view only from inside the panel, so Escape in the composer or a section keeps it.
    if (event.key === "Escape" && jobsOf && members?.contains(document.activeElement)) void closeJobs();
    else if (event.key === "Escape" && panel) void showPanel(false);
    else if (event.key === "Escape" && section) void closeSection();
    if (event.key !== "F6") return;
    event.preventDefault();
    // Only areas on screen take part; below 761 px the closed sessions panel has no boxes.
    // A panel from the room menu takes the place of the feed and the composer.
    const areas = [rail, section ? requests : feed, section ? undefined : composer, dock, members, docked ? undefined : computer]
      .filter(area => area != null)
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
  let roomName = $derived(attention.rooms.get(room ?? "")?.name ?? "Room");
  // A session's muted status line shows only state that matters: a running turn with its latest activity, or a
  // starting box. An idle session has none; its profile is the row's hover title.
  const status = (id: string) => {
    const box = store?.boxes.get(id);
    if (store?.turns.has(id)) { const label = store.activity.get(id); return label ? `working · ${label}` : "working"; }
    return box === "pending" || box === "creating" || box === "starting" ? "starting" : "";
  };
  // Below 761 px the sessions panel is hidden; a header button opens it over the feed.
  // Opening moves focus into the panel; closing returns it to the button if the panel held it.
  let panel = $state(false);
  // The session whose computer the owner watches, while it runs: in a window over the app, or docked
  // in the sessions panel instead of the list. Docked, the view mounts only where the panel is on
  // screen: always when wide, below 761 px only while it is open. The window keeps its place and size
  // for the page view.
  let watching: string | undefined = $state();
  let docked = $state(false);
  let rect = $state({ x: window.innerWidth - 504, y: window.innerHeight - 420, w: 480, h: 300 });
  // The first window opens at the bottom right of the feed, above the composer, as in v1; later ones keep the owner's rect.
  let placed = false;
  function watch(id: string) {
    const box = feed?.getBoundingClientRect();
    if (box && !placed) rect = { ...rect, x: box.right - rect.w - 16, y: box.bottom - rect.h - 16 };
    placed = true;
    watching = id;
  }
  // The view closes when its computer stops or the room changes, before the DOM updates, so it never
  // streams again unasked. Focus that was in the view returns to the row's button, or to the panel;
  // below 761 px with the panel closed, it returns to the panel's toggle button.
  async function stopWatching() {
    const id = watching ?? "";
    const held = members?.contains(document.activeElement) || computer?.contains(document.activeElement);
    watching = undefined;
    await tick();
    if (!held) return;
    if (!members?.getClientRects().length) toggle?.focus();
    else (members.querySelector<HTMLElement>(`[data-watch="${CSS.escape(id)}"]`) ?? members).focus();
  }
  $effect.pre(() => { if (watching && !store?.computers.has(watching)) void stopWatching(); });
  // The session whose jobs the panel shows instead of the list, while its box is ready; it closes as the computer does.
  let jobsOf: string | undefined = $state();
  const jobs = (id: string) => store?.boxes.get(id) === "ready" ? store.shellJobs.get(id) ?? 0 : 0;
  async function closeJobs() {
    const id = jobsOf ?? "", held = members?.contains(document.activeElement);
    jobsOf = undefined;
    await tick();
    if (held) (members?.querySelector<HTMLElement>(`[data-jobs="${CSS.escape(id)}"]`) ?? members)?.focus();
  }
  $effect.pre(() => { if (jobsOf && store?.boxes.get(jobsOf) !== "ready") void closeJobs(); });
  const wide = new MediaQuery("min-width: 761px");
  let toggle: HTMLButtonElement | undefined = $state();
  async function showPanel(open: boolean) {
    const held = members?.contains(document.activeElement);
    panel = open;
    if (open) { await tick(); members?.focus(); } else if (held) toggle?.focus();
  }
  // The sessions the feed composer offers after @: the root first, then by name. A DM offers none.
  let mentions = $derived(session ? [] : [...store?.sessions.values() ?? []].filter(mentionable)
    .map(item => ({ name: item.name, root: !item.parent, status: status(item.session) }))
    .sort((a, b) => Number(b.root) - Number(a.root) || a.name.localeCompare(b.name)));
  let title = $derived(!room ? "Home" : session ? store?.names.get(session) ?? "Session" : roomName);
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

{#snippet computerView(id: string)}
  <!-- Another session's view is a new view, so the old image runs its cleanup and drops its stream. -->
  {#key id}<ComputerView session={id} name={store?.names.get(id) ?? "Session"} {docked} bind:rect bind:element={computer}
    onclose={() => void stopWatching()} ondock={() => { docked = !docked; if (docked && !wide.current) void showPanel(true); }} />{/key}
{/snippet}

<svelte:window onkeydown={cycle} />
<div class="shell" class:open={room}>
  <aside class="rail" aria-label="Rooms" tabindex="-1" bind:this={rail}>
    <nav aria-label="Rooms"><a class="home" href="/" onclick={navigate} aria-current={room ? undefined : "page"} aria-label="Home" title="Home"><Icon name="home" /></a>
      <!-- A room's dot shows while a request waits for the owner. -->
      {#each rooms as item (item.id)}{@const waiting = !!attention.waiting.get(item.id)?.size}
      <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}
        aria-label={waiting ? `${item.name}, requests waiting` : item.name} title={item.name}>
        <Avatar kind="room" name={item.name} size={40} />{#if waiting}<span class="waiting"></span>{/if}</a>
    {/each}</nav>
    <footer><Avatar kind="owner" name="You" size={32} /></footer>
  </aside>
  <main>
    <header>{#if room && session}<a class="crumb" href={"/r/" + encodeURIComponent(room)} onclick={navigate}>{roomName}</a><span class="count">/</span>
      {:else if room}<Icon name="room" />{/if}<strong>{title}</strong>
      {#if closed}<span class="count">· closed</span>{/if}
      {#if store && !unavailable}<RoomMenu bind:this={menu} waiting={store.waiting} onchoose={next => { if (next === "terminal") void terminals?.pick(); else section = next; }} />{/if}
      {#if room}<button type="button" class="panel-toggle" bind:this={toggle} aria-label={store?.failed.size ? "Sessions, a turn failed" : "Sessions"}
        title="Sessions" aria-controls="sessions" aria-expanded={panel} onclick={() => void showPanel(!panel)}><Icon name="people" />
        {#if store?.failed.size && !panel}<span class="attention"></span>{/if}</button>{/if}</header>
    {#if section === "requests" && store && !unavailable}<RequestsPanel bind:element={requests} {store} room={roomName} onback={() => void closeSection()} />
    {:else}<div class="feed">
      {#if !room && !roomsError}<Home {attention} {navigate} onrequests={() => { section = "requests"; }} />
      {:else if store && !unavailable}
        {#key key}<Feed bind:this={view} {store} {home} bind:element={feed} {intro} unsent={unsent[key] ?? []} onunsent={keep} onsettled={settle}
          onforward={quote => { quotes[key] = quote; composer?.focus(); }} />{/key}
      {:else}<p class="empty">{unavailable || roomsError || (rooms.length ? "Select a room." : "Your rooms will appear here.")}
        {#if store && unavailable && !signedOut(store.error)}<button type="button" class="retry" onclick={() => void store?.open()}>Try again</button>{/if}</p>{/if}
    </div>
    {#if room && !unavailable}
      <Composer bind:element={composer} bind:value={() => drafts[key] ?? "", text => { drafts[key] = text; }} onsend={send} placeholder={"Message " + title}
        quote={quotes[key]} onunquote={() => { quotes[key] = undefined; }} sessions={mentions} />
    {/if}{/if}
    <TermDock bind:this={terminals} bind:element={dock} onempty={() => composer?.focus()}
      sessions={[...store?.sessions.values() ?? []].filter(item => store?.boxes.get(item.session) === "ready").map(item => ({ id: item.session, name: item.name }))} />
  </main>
  {#if room}
    <aside class="members" id="sessions" class:shown={panel} class:watching={jobsOf || (watching && docked)} aria-label="Sessions" tabindex="-1" bind:this={members}>
      {#if jobsOf}
        {#if wide.current || panel}<ShellView session={jobsOf} name={store?.names.get(jobsOf) ?? "Session"} onback={() => void closeJobs()} />{/if}
      {:else if watching && docked}
        {#if wide.current || panel}{@render computerView(watching)}{/if}
      {:else}
      <header class="label">Sessions</header>
      {#each tree as { session: item, depth, parent, hidden, hiddenTrouble } (item.session)}
        <div class="row">{#if parent}<button type="button" class="fold" aria-expanded={!hidden} style:left="{depth * 14}px"
          aria-label={hidden ? `Show ${count(hidden, "session")} under ${item.name}` : `Hide the sessions under ${item.name}`}
          title={hidden ? `${count(hidden, "session")} folded` : "Fold"} onclick={() => { if (!folded.delete(item.session)) folded.add(item.session); }}
          onkeydown={event => fold(event, item.session)}><Icon name="chevron" size={12} />{#if hidden}<span>{hidden}</span>{/if}</button>{/if}
        <a class="member" href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(item.session)}`} onclick={navigate}
          onkeydown={event => { walkTree(event); if (parent) fold(event, item.session); }}
          aria-current={session === item.session ? "page" : undefined} title={item.profile} style:padding-left="{28 + depth * 14}px">
          <SessionRow name={item.name} root={!item.parent} status={status(item.session)} />
          {#if trouble(item.session)}<StatusPill tone="failed">{store?.failed.has(item.session) ? "Failed" : trouble(item.session)}</StatusPill>
          {:else if hiddenTrouble}<StatusPill tone="failed">Failed</StatusPill>{/if}</a>
          {#if jobs(item.session)}<button type="button" class="watch" class:open={jobsOf === item.session} data-jobs={item.session} aria-label={`${item.name}'s background jobs, ${jobs(item.session)} running`}
            title={`${item.name}'s background jobs, ${jobs(item.session)} running`} onclick={() => { jobsOf = item.session; }}><Icon name="terminal" /></button>{/if}
          {#if store?.computers.has(item.session)}<button type="button" class="watch" class:open={watching === item.session} data-watch={item.session} aria-label={`Watch ${item.name}'s computer`}
            title={`Watch ${item.name}'s computer`} onclick={() => watch(item.session)}><Icon name="monitor" /></button>{/if}</div>
      {:else}<p class="hint">No sessions yet.</p>{/each}
      {/if}
    </aside>
    {#if watching && !docked}{@render computerView(watching)}{/if}
  {/if}
</div>

<style>
  a.member { color: var(--text); text-decoration: none; }
  a.member[aria-current="page"] { background: var(--selected); }
  .row { position: relative; display: flex; align-items: center; }
  /* The chevron sits in the gutter left of the avatar. As in v1, a folded row's hidden count is part of the same
     button, but it sits on the row line beside the chevron. */
  .fold { all: unset; position: absolute; z-index: 1; top: 50%; transform: translateY(-50%); min-width: 16px; min-height: 24px; display: flex; align-items: center; justify-content: center;
    padding: 0 1px; border-radius: 4px; color: var(--muted); font-size: 10px; line-height: 1.1; font-variant-numeric: tabular-nums; cursor: pointer; }
  .fold:hover { background: var(--hover); color: var(--text); }
  .fold:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .fold[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  /* A thumb gets v1's taller target, so a near miss does not open the DM. */
  @media (hover: none) { .fold { width: 24px; min-height: 44px; } }
  .row a.member { flex: 1; min-width: 0; }
  .watch { all: unset; display: grid; place-items: center; width: 24px; height: 24px; margin-right: 8px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .watch:hover { background: var(--hover); color: var(--text); }
  .watch:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* The name wins: a pointer shows a row's icons on hover, and the keyboard on focus; an open view keeps its icon. */
  @media (hover: hover) { .row:not(:hover, :focus-within) .watch:not(.open) { display: none; } }
  /* The docked computer and the jobs view need 420 px. They dock beside the feed from 1024 px, where the
     feed keeps 540 px; below that they cover the feed, and Back or Escape closes them. */
  @media (min-width: 1024px) { .shell.open:has(aside.watching) { grid-template-columns: 64px 1fr 420px; } }
  @media (min-width: 761px) and (max-width: 1023px) { .shell > aside.members.watching { position: fixed; top: var(--panel-top, 52px); right: 0; bottom: 0; width: 420px; z-index: 1; box-shadow: -8px 0 24px rgb(0 0 0 / 25%); } }
  @media (max-width: 760px) { .shell > aside.members.shown.watching { width: min(420px, 100vw); } }
</style>
