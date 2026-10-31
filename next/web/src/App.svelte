<script lang="ts">
  import { tick, untrack } from "svelte";
  import { RoomStore } from "./lib/room.svelte.ts";
  import { blockedBy, retryDelay, type Blocked as BlockedState } from "./lib/client.ts";
  import Blocked from "./lib/ui/Blocked.svelte";
  import Feed from "./lib/ui/Feed.svelte";
  import StatusPill from "./lib/ui/StatusPill.svelte";
  import Composer from "./lib/ui/Composer.svelte";
  import SessionRow from "./lib/ui/SessionRow.svelte";
  import HostFoot from "./lib/ui/HostFoot.svelte";
  import VirtualList from "./lib/ui/VirtualList.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import Avatar, { setSessionLabels } from "./lib/ui/Avatar.svelte";
  import RoomIntro from "./lib/ui/RoomIntro.svelte";
  import { causes, count, quoted, type Quote } from "./lib/feed.ts";
  import { urgency, type Status } from "./lib/status.ts";
  import { FileDraft } from "./lib/attach.svelte.ts";
  import ComputerView from "./lib/ui/ComputerView.svelte";
  import ShellView from "./lib/ui/ShellView.svelte";
  import TermDock from "./lib/ui/TermDock.svelte";
  import Home from "./lib/ui/Home.svelte";
  import { MediaQuery, SvelteSet } from "svelte/reactivity";
  import RoomMenu from "./lib/ui/RoomMenu.svelte";
  import RequestsPanel from "./lib/ui/RequestsPanel.svelte";
  import { Attention } from "./lib/attention.svelte.ts";
  import { mentionable, parseMentions } from "./lib/mentions.ts";
  import { distinctInitials } from "./lib/identity.ts";
  import type { Conversation, PostRequest, SessionStarted } from "./lib/contract.gen.ts";
  const attention = new Attention();
  let rooms = $derived([...attention.rooms.values()]);
  // Distinct initials for every room avatar, so two rooms never look alike on the rail.
  let initials = $derived(distinctInitials(rooms.map(item => item.name)));
  // While the engine is not reachable, open runs again by itself: after a growing wait while the page is visible, and at
  // once when a hidden page becomes visible again. Try again runs it at once.
  // An open runs alone, so Try again or a wake during one adds no second stream; none starts after stop.
  function retrying(open: () => Promise<BlockedState | undefined>) {
    let tries = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let pending = false, stopped = false, failing = false;
    const run = () => {
      if (pending || stopped) return;
      clearTimeout(timer);
      pending = true;
      void open().then(state => {
        pending = false;
        failing = state === "unreachable";
        if (stopped) return;
        if (!failing) { tries = 0; return; }
        if (document.visibilityState === "visible") timer = setTimeout(run, retryDelay(tries++));
      });
    };
    const wake = () => { if (document.visibilityState === "hidden") clearTimeout(timer); else if (failing) run(); };
    document.addEventListener("visibilitychange", wake);
    return { run, stop: () => { stopped = true; clearTimeout(timer); document.removeEventListener("visibilitychange", wake); } };
  }
  // room.list failing blocks the whole page: the owner is signed out, or the engine is not reachable.
  let blocked: BlockedState | undefined = $state();
  let roomsLoaded = $state(false);
  const connect = retrying(() => attention.open().then(() => { blocked = undefined; roomsLoaded = true; return undefined; },
    (error: unknown) => (blocked = blockedBy(error) ?? "unreachable"))).run;
  connect();
  let path = $state(window.location.pathname);
  window.addEventListener("popstate", () => { path = window.location.pathname; section = undefined; });
  // The requests view shows in place of the conversation until Back, Escape or its header button.
  let section: "requests" | undefined = $state();
  let requestsButton: HTMLButtonElement | undefined = $state();
  async function closeSection() {
    section = undefined;
    await tick();
    requestsButton?.focus();
  }
  const segment = (pattern: RegExp) => { const match = pattern.exec(path)?.[1]; return match && decodeURIComponent(match); };
  let room = $derived(segment(/^\/r\/([^/]+)/));
  let session = $derived(segment(/^\/r\/[^/]+\/s\/([^/]+)/));
  let store: RoomStore | undefined = $state();
  // The same rule per room for its open sessions.
  let sessionInitials = $derived(distinctInitials([...store?.sessions.values() ?? []].map(item => item.name)));
  setSessionLabels(() => sessionInitials);
  $effect(() => {
    // Home has no room, so no room controls or panels may show one.
    if (!room) { store = undefined; return; }
    const current = new RoomStore(room);
    store = current;
    const opening = retrying(() => current.open().then(() => current.ready ? undefined : blockedBy(current.error)));
    reopen = opening.run;
    opening.run();
    return () => { opening.stop(); current.close(); };
  });
  // A room that failed to open is unavailable, for the same reasons as the page or its own; later load errors show inside the feed.
  // Opens the shown room again; the effect above sets it for each room.
  let reopen = $state(() => {});
  let unavailable: BlockedState | "room" | undefined = $derived(!store || store.ready || store.error === undefined ? undefined : blockedBy(store.error) ?? "room");
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
  // Siblings sort by the most urgent status in their subtree: what needs the owner, then working, then idle. Two or
  // more idle siblings fold into one "N idle" row that opens in place, so a room of 100 sessions shows its work first.
  const folded = new SvelteSet<string>();
  const openIdle = new SvelteSet<string>();
  type TreeRow = { kind: "session"; session: SessionStarted; depth: number; parent: boolean; hidden: number; state: Status; dot: Status; mark?: string }
    | { kind: "idle"; key: string; depth: number; count: number; open: boolean };
  const treeKey = (entry: TreeRow) => entry.kind === "idle" ? `idle ${entry.key}` : entry.session.session;
  let sessionList: VirtualList<TreeRow> | undefined = $state();
  const folds: Record<Status, string> = { waiting: "A folded session waits for you", failed: "A folded session failed", working: "A folded session is working", idle: "" };
  let tree = $derived.by(() => {
    const sessions = [...(store?.sessions.values() ?? [])];
    const ids = new Set(sessions.map(item => item.session));
    const children = (parent: string | undefined) => sessions.filter(item => (item.parent && ids.has(item.parent) ? item.parent : undefined) === parent);
    const under = (parent: string): string[] => children(parent).flatMap(item => [item.session, ...under(item.session)]);
    const own = new Map(sessions.map(item => [item.session, store?.status(item.session) ?? "idle"]));
    const most = (list: string[]) => list.reduce<Status>((best, id) => urgency(own.get(id) ?? "idle") < urgency(best) ? own.get(id) ?? "idle" : best, "idle");
    const top = new Map(sessions.map(item => [item.session, most([item.session, ...under(item.session)])]));
    // The open DM's row and its ancestors never fold away as idle.
    const keep = new Set<string>();
    for (let id = session; id && ids.has(id); id = store?.sessions.get(id)?.parent) keep.add(id);
    const rows: TreeRow[] = [];
    const walk = (parent: string | undefined, depth: number) => {
      const kids = children(parent).sort((a, b) => urgency(top.get(a.session) ?? "idle") - urgency(top.get(b.session) ?? "idle"));
      const idle = kids.filter(item => top.get(item.session) === "idle" && !keep.has(item.session));
      const key = parent ?? "";
      const open = idle.length < 2 || openIdle.has(key);
      for (const item of kids) {
        if (item === idle[0] && idle.length >= 2) rows.push({ kind: "idle", key, depth, count: idle.length, open });
        if (!open && idle.includes(item)) continue;
        const hidden = folded.has(item.session) ? under(item.session) : [];
        // A folded row shows the most urgent status it hides when that is more urgent than its own.
        const inside = most(hidden), state = own.get(item.session) ?? "idle";
        const shown = urgency(inside) < urgency(state) ? inside : state;
        rows.push({ kind: "session", session: item, depth, parent: children(item.session).length > 0, hidden: hidden.length, state, dot: shown,
          mark: shown === state ? undefined : folds[shown] });
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
  // Each conversation keeps its own draft files; FileDraft is reactive inside, so a plain map holds them.
  const fileDrafts = new Map<string, FileDraft>();
  const files = (key: string) => { let draft = fileDrafts.get(key); if (!draft) fileDrafts.set(key, draft = new FileDraft()); return draft; };
  let key = $derived(`${room}/${session ?? ""}`);
  function send() {
    if (!store || !room) return;
    const text = drafts[key] ?? "", quote = quotes[key], draft = files(key);
    if (draft.busy || (!text.trim() && !quote && !draft.items.length)) return;
    const attachments = [...quote?.attachments ?? [], ...draft.take()];
    drafts[key] = "";
    quotes[key] = undefined;
    // The engine refuses mentions on a DM post; in the feed, @names of open sessions become mentions.
    // Mentions come from the owner's own words only, so an @name inside a forwarded post wakes nobody.
    const post = session ? { text, mentions: [] } : parseMentions(text, store.sessions.values());
    // A forward carries the post's files; the engine keeps each file once, by hash, for every conversation.
    void view?.send({ room, text: quote ? quoted(quote, post.text) : post.text, mentions: post.mentions.length ? post.mentions : undefined,
      home: session ? home : undefined, attachments: attachments.length ? attachments : undefined });
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
    if (event.key === "Escape" && drawer) void showDrawer(false);
    else if (event.key === "Escape" && jobsOf && members?.contains(document.activeElement)) void closeJobs();
    else if (event.key === "Escape" && panel) void showPanel(false);
    else if (event.key === "Escape" && section) void closeSection();
    if (event.key !== "F6") return;
    event.preventDefault();
    // Only areas on screen take part; below 761 px the closed sessions panel has no boxes.
    // The requests view takes the place of the feed and the composer.
    const areas = [rail, section ? requests : feed, section ? undefined : composer, dock, members, docked ? undefined : computer]
      .filter(area => area != null)
      .filter(area => area.getClientRects().length > 0);
    const index = areas.findIndex(area => area.contains(document.activeElement));
    areas[(index + (event.shiftKey ? areas.length - 1 : 1)) % areas.length].focus();
  }
  // Up and Down move between the sessions in the tree and its idle folds; Enter opens one.
  // The tree is virtual, so the next row may not be rendered yet: the list scrolls to it first. A held key's presses
  // that come meanwhile move on from that row, and only the newest one focuses.
  let walkingTo: number | undefined;
  // Focus goes back to a row's icon when a view over the list closes. The list mounts again at its top, so the row is
  // scrolled back in first; and a row shows its icons only on hover or focus, so its link takes focus before the icon.
  async function focusRowIcon(id: string, icon: string) {
    const index = tree.findIndex(entry => entry.kind === "session" && entry.session.session === id);
    if (index >= 0) await sessionList?.reveal(index);
    const button = members?.querySelector<HTMLElement>(`[${icon}="${CSS.escape(id)}"]`);
    button?.closest(".row")?.querySelector<HTMLElement>("a.member")?.focus();
    (button ?? members)?.focus();
  }
  async function walkTree(event: KeyboardEvent & { currentTarget: HTMLElement }) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" || !members) return;
    event.preventDefault();
    const index = walkingTo ?? tree.findIndex(entry => treeKey(entry) === event.currentTarget.dataset.key);
    const next = Math.max(0, Math.min(tree.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)));
    const target = () => members?.querySelector<HTMLElement>(`[data-key="${CSS.escape(treeKey(tree[next]))}"]`);
    walkingTo = next;
    if (!target()) await sessionList?.reveal(next);
    if (walkingTo !== next) return;
    walkingTo = undefined;
    target()?.focus();
  }
  let roomName = $derived(attention.rooms.get(room ?? "")?.name ?? "Room");
  // A session's line says what it does, from its one status (status.ts). A failed session's line is the reason
  // after "Failed", which the row draws; an idle one says Starting while its box starts, else Idle. Its profile is the
  // row's hover title.
  const status = (id: string) => {
    const box = store?.boxes.get(id), cause = store?.causes.get(id);
    switch (store?.status(id)) {
      case "waiting": return "Waiting for your approval";
      case "working": { const label = store?.activity.get(id); return label ? `Working · ${label}` : "Working"; }
      case "failed": {
        const reason = box === "failed" ? "could not start" : box === "lost" ? "stopped unexpectedly" : cause ? causes[cause] : "the last turn failed";
        return reason[0].toLowerCase() + reason.slice(1);
      }
      default: return box === "pending" || box === "creating" || box === "starting" ? "Starting" : "Idle";
    }
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
    else await focusRowIcon(id, "data-watch");
  }
  $effect.pre(() => { if (watching && !store?.computers.has(watching)) void stopWatching(); });
  // The session whose jobs the panel shows instead of the list, while its box is ready; it closes as the computer does.
  let jobsOf: string | undefined = $state();
  const jobs = (id: string) => store?.boxes.get(id) === "ready" ? store.shellJobs.get(id) ?? 0 : 0;
  async function closeJobs() {
    const id = jobsOf ?? "", held = members?.contains(document.activeElement);
    jobsOf = undefined;
    await tick();
    if (held) await focusRowIcon(id, "data-jobs");
  }
  $effect.pre(() => { if (jobsOf && store?.boxes.get(jobsOf) !== "ready") void closeJobs(); });
  const wide = new MediaQuery("min-width: 761px");
  // Below 641 px the rail is a drawer, as in Discord's phone app: the rooms on the left and the open room's pages beside
  // them. A pick closes it; the scrim and Escape close it and return focus to the menu button. A keyboard open or
  // close (Escape) shows the ring; a tap open focuses the dialog itself, and no ring follows a tap either way.
  const phone = new MediaQuery("max-width: 640px");
  let drawer = $state(false);
  let menuButton: HTMLButtonElement | undefined = $state();
  async function showDrawer(open: boolean, refocus = true, tapped = false) {
    drawer = open;
    await tick();
    if (open && tapped) rail.focus({ focusVisible: false });
    else if (open) (rail.querySelector<HTMLElement>(".pages [aria-current=page]") ?? rail.querySelector<HTMLElement>(".pages a"))?.focus();
    else if (refocus) menuButton?.focus({ focusVisible: !tapped });
  }
  $effect(() => { if (!phone.current) drawer = false; });
  // On a phone the sessions panel is a bottom sheet at about two thirds of the height (fix 1). Its handle drags it to
  // full height, or down to its first height and then closed; a tap toggles the height. The tree already puts what
  // needs the owner first and folds idle sessions.
  let sheetFull = $state(false), dragged = false;
  function drag(event: PointerEvent & { currentTarget: HTMLButtonElement }) {
    const start = event.clientY, handle = event.currentTarget;
    handle.setPointerCapture(event.pointerId);
    handle.onpointerup = up => {
      handle.onpointerup = null;
      const dy = up.clientY - start;
      dragged = Math.abs(dy) > 8;
      if (dy < -40) sheetFull = true;
      else if (dy > 60 && sheetFull) sheetFull = false;
      else if (dy > 60) void showPanel(false, true);
    };
  }
  let toggle: HTMLButtonElement | undefined = $state();
  // As the drawer does: after a tap, focus moves with no ring. Focus waits for the update, which takes inert off main
  // when a phone's sheet closes.
  async function showPanel(open: boolean, tapped = false) {
    const held = members?.contains(document.activeElement);
    panel = open;
    if (!open) sheetFull = false;
    await tick();
    if (open) members?.focus({ focusVisible: !tapped }); else if (held) toggle?.focus({ focusVisible: !tapped });
  }
  // The sessions the feed composer offers after @: the root first, then by name. A DM offers none.
  let mentions = $derived(session ? [] : [...store?.sessions.values() ?? []].filter(mentionable)
    .map(item => { const state = store?.status(item.session);
      return { name: item.name, root: !item.parent, status: state === "idle" ? "" : state === "failed" ? `Failed · ${status(item.session)}` : status(item.session) }; })
    .sort((a, b) => Number(b.root) - Number(a.root) || a.name.localeCompare(b.name)));
  // The panel's header counts, and the room header's below 761 px where the panel is hidden.
  // One count source (status.ts): a session that waits for the owner is waiting, not working.
  let working = $derived(store?.counts.working ?? 0);
  let title = $derived(!room ? "Home" : session ? store?.names.get(session) ?? "Session" : roomName);
  // Each fact shows once: profile, state and trouble live on the session's row. A closed session has no row.
  let closed = $derived(!!session && !!store?.ready && !store.sessions.has(session));
  // room.list gives the feed's read position. Opening a session's DM shows the owner its failure, which clears the mark.
  $effect(() => { const read = attention.rooms.get(room ?? "")?.lastRead; if (store && read) store.noteRead({ kind: "feed" }, read); });
  // Only opening counts: a failure that arrives while the DM is open clears once its row is on screen.
  $effect(() => {
    const open = session, current = store;
    if (open && current?.ready) untrack(() => { if (current.unseen(open)) current.see({ kind: "dm", session: open }, current.failed.get(open) ?? 0); });
  });
</script>

{#snippet intro()}
  {#if session}<RoomIntro kind="session" name={title} root={!!store?.sessions.get(session) && !store.sessions.get(session)?.parent} line={`This is the start of your direct messages with ${title}.`} />
  {:else}{@const created = attention.rooms.get(room ?? "")?.created}
    <RoomIntro kind="room" name={roomName} label={initials.get(roomName)} line={`This is the beginning of #${roomName}.${created ? ` It started ${new Date(created).toLocaleDateString([], { month: "long", day: "numeric", year: "numeric" })}.` : ""}`} />{/if}
{/snippet}

{#snippet computerView(id: string)}
  <!-- Another session's view is a new view, so the old image runs its cleanup and drops its stream. -->
  {#key id}<ComputerView session={id} name={store?.names.get(id) ?? "Session"} activity={store?.activity.get(id)} {docked} bind:rect bind:element={computer}
    onclose={() => void stopWatching()} ondock={() => { docked = !docked; if (docked && !wide.current) void showPanel(true); }} />{/key}
{/snippet}

<svelte:window onkeydown={cycle} />
<div class="shell" class:open={room} class:drawer>
  <aside class="rail" aria-label="Rooms" tabindex="-1" bind:this={rail} inert={phone.current && !drawer} role={drawer ? "dialog" : undefined} aria-modal={drawer || undefined}
    onclick={event => { if (drawer && event.target instanceof Element && event.target.closest("a, button")) void showDrawer(false, false); }}>
    <nav aria-label="Rooms"><a class="home" href="/" onclick={navigate} aria-current={room ? undefined : "page"} aria-label="Home" title="Home"><Icon name="home" /></a>
      <!-- A room with requests that wait for the owner gets an amber dot on its corner, with no number (spec 5.13). -->
      {#each rooms as item (item.id)}{@const waiting = attention.waiting.get(item.id)?.size ?? 0}
      <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id ? "page" : undefined}
        aria-label={waiting ? `${item.name}, ${count(waiting, "request")} waiting` : item.name} title={item.name}>
        <Avatar kind="room" name={item.name} label={initials.get(item.name)} size={40} />{#if waiting}<StatusPill tone="warning" label="Waiting" dot />{/if}</a>
    {/each}</nav>
    <!-- The owner's face from the feed; v2 has no owner name or account menu yet. -->
    <footer><span role="img" aria-label="You" title="You"><Avatar kind="owner" name="You" size={32} /></span></footer>
    <!-- The drawer's second column: the open room's pages, or on Home every room by name. Counts are muted; only Requests
         uses the warning tone. -->
    {#if phone.current}<nav class="pages" aria-label={room ? `${roomName} pages` : "Rooms by name"}><strong>{room ? roomName : "Home"}</strong>
      {#if room}
        <a href={"/r/" + encodeURIComponent(room)} onclick={navigate} aria-current={!session && !section ? "page" : undefined}><Icon name="chat" />Chat</a>
        {#if store && !unavailable}
          <button type="button" aria-pressed={section === "requests"} onclick={() => { section = "requests"; }}><Icon name="inbox" />Requests
            {#if store.waiting}<StatusPill tone="warning" label="Waiting" count={store.waiting} />{/if}</button>
          <button type="button" onclick={event => void showPanel(true, event.detail > 0)}><Icon name="people" />Sessions<span class="count">{store.sessions.size}</span></button>
          <button type="button" onclick={() => void terminals?.pick()}><Icon name="terminal" />Terminal</button>
        {/if}
      {:else}
        {#each rooms as item (item.id)}{@const waiting = attention.waiting.get(item.id)?.size ?? 0}
          <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate}><Avatar kind="room" name={item.name} label={initials.get(item.name)} size={28} />{item.name}
            {#if waiting}<StatusPill tone="warning" label="Waiting" count={waiting} />{/if}</a>
        {/each}
      {/if}</nav>{/if}
  </aside>
  {#if drawer || (panel && phone.current)}<button type="button" class="scrim" tabindex="-1" aria-label={drawer ? "Close the menu" : "Close the sessions"}
    onclick={() => void (drawer ? showDrawer(false, true, true) : showPanel(false, true))}></button>{/if}
  <main inert={drawer || (panel && phone.current)}>
    <header><button type="button" class="menu-toggle" bind:this={menuButton} aria-label="Rooms and pages" title="Rooms and pages" aria-expanded={drawer}
        onclick={event => void showDrawer(true, true, event.detail > 0)}><Icon name="menu" /></button>
      {#if room && session}<a class="crumb" href={"/r/" + encodeURIComponent(room)} onclick={navigate}>{roomName}</a><span class="count">/</span>
      {:else if room}<span class="hash"><Icon name="room" /></span>{/if}
      <span class="heading"><strong>{title}</strong>
        {#if closed}<span class="count">· closed</span>{/if}
        {#if store?.ready && !session}<span class="count narrow">{count(store.sessions.size, "session") + (working ? ` · ${working} working` : "")}</span>{/if}</span>
      {#if store && !unavailable}<button type="button" class="requests" class:waiting={store.waiting > 0} bind:this={requestsButton} aria-pressed={section === "requests"}
        aria-label={store.waiting ? `Requests, ${store.waiting} waiting` : "Requests"} title="Requests" onclick={() => { section = section ? undefined : "requests"; }}>
        <Icon name="inbox" />{#if store.waiting}<StatusPill tone="warning" label="Waiting" count={store.waiting} />{/if}</button>
        <span class="room-menu"><RoomMenu onterminal={() => void terminals?.pick()} /></span>{/if}
      {#if room}<button type="button" class="panel-toggle" bind:this={toggle} aria-label={store?.counts.failed ? "Sessions, a session failed" : "Sessions"}
        title="Sessions" aria-controls="sessions" aria-expanded={panel} onclick={event => void showPanel(!panel, event.detail > 0)}><Icon name="people" />{#if store?.ready}<span class="count phone">{store.sessions.size}</span>{/if}
        {#if store?.counts.failed && !panel}<span class="attention"></span>{/if}</button>{/if}</header>
    {#if section === "requests" && store && !unavailable}<RequestsPanel bind:element={requests} {store} room={roomName} onback={() => void closeSection()} />
    {:else}<div class="feed">
      {#if blocked}<Blocked kind={blocked} onretry={blocked === "unreachable" ? connect : undefined} />
      {:else if !room && roomsLoaded && !rooms.length}<Blocked kind="no-rooms" />
      {:else if !room}<Home {attention} {initials} loaded={roomsLoaded} {navigate} onrequests={() => { section = "requests"; }} />
      {:else if unavailable && unavailable !== "room"}<Blocked kind={unavailable} onretry={unavailable === "unreachable" ? reopen : undefined} />
      {:else if store && !unavailable}
        {#key key}<Feed bind:this={view} {store} {home} bind:element={feed} {intro} unsent={unsent[key] ?? []} onunsent={keep} onsettled={settle}
          onforward={quote => { quotes[key] = quote; composer?.focus(); }} />{/key}
      {:else}<p class="empty">This room is not available. <button type="button" class="retry" onclick={reopen}>Try again</button></p>{/if}
    </div>
    {#if room && !unavailable}
      <Composer bind:element={composer} bind:value={() => drafts[key] ?? "", text => { drafts[key] = text; }} onsend={send} placeholder={"Message " + title}
        quote={quotes[key]} onunquote={() => { quotes[key] = undefined; }} sessions={mentions} files={files(key)} />
    {/if}{/if}
    <TermDock bind:this={terminals} bind:element={dock} onempty={() => composer?.focus()}
      sessions={[...store?.sessions.values() ?? []].filter(item => store?.boxes.get(item.session) === "ready").map(item => ({ id: item.session, name: item.name }))} />
  </main>
  {#if room}
    <aside class="members" id="sessions" class:shown={panel} class:full={sheetFull} class:watching={jobsOf || (watching && docked)} aria-label="Sessions" tabindex="-1" bind:this={members} inert={drawer}>
      {#if jobsOf}
        {#if wide.current || panel}<ShellView session={jobsOf} name={store?.names.get(jobsOf) ?? "Session"} onback={() => void closeJobs()} />{/if}
      {:else}
      <!-- Docked, the computer sits above the session list. -->
      {#if watching && docked && (wide.current || panel)}{@render computerView(watching)}{/if}
      {#if phone.current}<button type="button" class="handle" aria-label={sheetFull ? "Shrink the sessions sheet" : "Expand the sessions sheet"}
        onpointerdown={drag} onclick={() => { if (!dragged) sheetFull = !sheetFull; dragged = false; }}></button>{/if}
      <header><strong>Sessions</strong>{#if store?.ready}<span class="count">{store.sessions.size + (working ? ` · ${working} working` : "")}</span>{/if}</header>
      <!-- A long tree renders only its visible rows, through the feed's shared list (spec: long lists are virtual). -->
      {#if tree.length}<div class="tree"><VirtualList bind:this={sessionList} items={tree} key={treeKey}
        label="Session list" role="group" align="top" estimate={48}>
        {#snippet row(entry: TreeRow)}
        {#if entry.kind === "idle"}<button type="button" class="idle" data-key={treeKey(entry)} aria-expanded={entry.open} style:padding-left="{entry.depth * 14}px" onkeydown={walkTree}
          onclick={() => { if (!openIdle.delete(entry.key)) openIdle.add(entry.key); }}><Icon name="chevron" size={12} />{count(entry.count, "idle session")}</button>
        {:else}{@const { session: item, depth, parent, hidden, state, dot, mark } = entry}
        <div class="row">{#if parent}<button type="button" class="fold" aria-expanded={!hidden} style:left="{depth * 14}px"
          aria-label={hidden ? `Show ${count(hidden, "session")} under ${item.name}` : `Hide the sessions under ${item.name}`}
          title={hidden ? `${count(hidden, "session")} folded` : "Fold"} onclick={() => { if (!folded.delete(item.session)) folded.add(item.session); }}
          onkeydown={event => fold(event, item.session)}><Icon name="chevron" size={12} />{#if hidden}<span>{hidden}</span>{/if}</button>{/if}
        <a class="member" data-key={item.session} href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(item.session)}`}
          onclick={event => { navigate(event); if (!wide.current && event.defaultPrevented) void showPanel(false); }}
          onkeydown={event => { void walkTree(event); if (parent) fold(event, item.session); }}
          aria-current={session === item.session ? "page" : undefined} title={item.profile} style:padding-left="{28 + depth * 14}px">
          <SessionRow name={item.name} root={!item.parent} {state} {dot} line={status(item.session)} {mark} /></a>
          {#if jobs(item.session)}<button type="button" class="watch" class:open={jobsOf === item.session} data-jobs={item.session} aria-label={`${item.name}'s background jobs, ${jobs(item.session)} running`}
            title={`${item.name}'s background jobs, ${jobs(item.session)} running`} onclick={() => { jobsOf = item.session; }}><Icon name="terminal" /></button>{/if}
          {#if store?.computers.has(item.session)}<button type="button" class="watch" class:open={watching === item.session} data-watch={item.session} aria-label={`Watch ${item.name}'s computer`}
            title={`Watch ${item.name}'s computer`} onclick={() => watch(item.session)}><Icon name="monitor" /></button>{/if}</div>{/if}
        {/snippet}</VirtualList></div>
      {:else}<p class="hint">No sessions yet.</p>{/if}
      <!-- The foot polls hosts, so it mounts only while the panel shows; a phone's closed panel polls nothing. -->
      {#if wide.current || panel}<HostFoot {navigate} />{/if}
      {/if}
    </aside>
    {#if watching && !docked}{@render computerView(watching)}{/if}
  {/if}
</div>

<style>
  /* Two-line rows around the 28 px avatar, as in the chosen mockup: the name, then what the session does. */
  a.member { min-height: 48px; color: var(--text); text-decoration: none; }
  /* The panel's header matches the room header: a 16 px title with its muted counts beside it. */
  aside.members > header strong { font-size: 16px; }
  /* Requests is a header icon, as the menu and the panel toggle are: 16 px in a 28 px hit area. While requests wait for
     the owner it is one 40 px chip: the icon and StatusPill's count on the warning wash. */
  .requests { margin-left: auto; display: flex; align-items: center; justify-content: center; gap: 6px; min-width: 28px; height: 28px; padding: 0;
    border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .requests:hover, .requests[aria-pressed="true"] { background: var(--hover); color: var(--text); }
  .requests.waiting { height: 40px; padding: 0 12px; border-radius: 8px; background: color-mix(in srgb, var(--attention) 14%, transparent); color: var(--attention); }
  .requests.waiting:hover, .requests.waiting[aria-pressed="true"] { background: color-mix(in srgb, var(--attention) 22%, transparent); }
  .requests ~ :global(.panel-toggle) { margin-left: 0; }
  .narrow, .phone, .menu-toggle { display: none; }
  @media (max-width: 760px) { .narrow { display: inline; } }
  .heading, .room-menu { display: contents; }
  /* A phone's header: the menu button, the title over its muted counts, Requests only while something waits (40 px
     targets), and the Sessions button with its count. The room menu's one action, Terminal, moves into the drawer. */
  @media (max-width: 640px) {
    .menu-toggle { display: grid; place-items: center; flex: none; width: 40px; height: 40px; margin-left: -10px; padding: 0; border: 0; border-radius: 8px;
      background: none; color: var(--muted); cursor: pointer; }
    /* Touch screens keep :hover after a tap, so the wash waits for a real pointer. */
    @media (hover: hover) { .menu-toggle:hover { background: var(--hover); color: var(--text); } }
    .hash, .room-menu { display: none; }
    .heading { display: flex; flex: 1; flex-direction: column; min-width: 0; line-height: 1.25; }
    .heading strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .heading .count { font-size: 12.5px; }
    .requests { min-width: 40px; height: 40px; }
    .requests:not([aria-pressed="true"], .waiting) { display: none; }
    main > header :global(.panel-toggle) { display: flex; align-items: center; gap: 4px; min-width: 40px; height: 40px; padding: 0 8px; }
    .phone { display: inline; }
    .scrim { position: fixed; inset: 0; z-index: 2; padding: 0; border: 0; background: rgb(0 0 0 / 50%); }
  }
  .handle { display: none; }
  @media (max-width: 640px) {
    .handle { all: unset; position: relative; display: block; width: 100%; height: 22px; cursor: grab; touch-action: none; }
    .handle::before { content: ""; position: absolute; left: calc(50% - 18px); top: 9px; width: 36px; height: 4px; border-radius: 2px; background: color-mix(in srgb, var(--muted) 45%, transparent); }
    .handle:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
    aside.members > header { border-bottom: 0; }
  }
  .pages { grid-column: 2; grid-row: 1 / 3; display: flex; flex-direction: column; gap: 2px; min-width: 0; padding: 12px 8px; border-left: 1px solid var(--line); overflow-y: auto; }
  .pages strong { padding: 6px 10px 10px; font-size: 16px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .pages a, .pages button { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 0 10px; border-radius: 8px;
    color: var(--muted); font-size: 15px; cursor: pointer; }
  @media (hover: hover) { .pages a:hover, .pages button:hover { background: var(--hover); box-shadow: none; color: var(--text); } }
  .pages a[aria-current="page"], .pages button[aria-pressed="true"] { background: var(--selected); color: var(--text); font-weight: 500; }
  .pages a::before { content: none; }
  .pages a:focus-visible, .pages button:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .pages .count, .pages :global(.count) { position: static; margin-left: auto; }
  a.member[aria-current="page"] { background: var(--selected); }
  .tree { flex: 1; min-height: 0; }
  .row { position: relative; display: flex; align-items: center; }
  /* The chevron sits in the gutter left of the avatar. As in v1, a folded row's hidden count is part of the same
     button, but it sits on the row line beside the chevron. */
  .fold { all: unset; position: absolute; z-index: 1; top: 50%; transform: translateY(-50%); min-width: 16px; min-height: 24px; display: flex; align-items: center; justify-content: center;
    padding: 0 1px; border-radius: 4px; color: var(--muted); font-size: 11px; line-height: 1.1; font-variant-numeric: tabular-nums; cursor: pointer; }
  .fold:hover { background: var(--hover); color: var(--text); }
  .fold:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .fold[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  /* A thumb gets v1's taller target, so a near miss does not open the DM. */
  @media (hover: none) { .fold { width: 24px; min-height: 44px; } .idle { height: 44px; } }
  .row a.member { flex: 1; min-width: 0; }
  /* "N idle sessions" stands for idle siblings until opened. Its chevron sits in the fold gutter and its words line
     up with the names. */
  .idle { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 12px; width: 100%; height: 32px; color: var(--muted);
    font-size: 12.5px; cursor: pointer; }
  .idle :global(svg) { flex: none; width: 16px; }
  .idle[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  .idle:hover { background: var(--hover); color: var(--text); }
  .idle:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .watch { all: unset; display: grid; place-items: center; width: 24px; height: 24px; margin-right: 8px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .watch:hover { background: var(--hover); color: var(--text); }
  .watch:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* The name wins: a pointer shows a row's icons on hover, and the keyboard on focus; an open view keeps its icon. */
  @media (hover: hover) { .row:not(:hover, :focus-within) .watch:not(.open) { display: none; } }
  /* The docked computer and the jobs view need 420 px. They dock beside the feed from 1024 px, where the
     feed keeps 540 px; below that they cover the feed, and Back or Escape closes them. */
  @media (min-width: 1024px) { .shell.open:has(aside.watching) { grid-template-columns: 64px 1fr 420px; } }
  @media (min-width: 761px) and (max-width: 1023px) { .shell > aside.members.watching { position: fixed; top: calc(var(--panel-top, 52px) + env(safe-area-inset-top)); right: 0; bottom: 0; width: 420px; z-index: 1; box-shadow: -8px 0 24px rgb(0 0 0 / 25%); } }
  @media (max-width: 760px) { .shell > aside.members.shown.watching { width: min(420px, 100vw); } }
</style>
