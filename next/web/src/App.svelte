<script lang="ts">
  import { watchHello, watchStreamState } from "./lib/stream.ts";
  import { watchConnect } from "./lib/connect-watch.ts";
  import { reconnectNotice } from "./lib/reconnect.ts";
  import { FormDrafts } from "./lib/form-drafts.ts";
  import { ComposerDrafts } from "./lib/composer-drafts.ts";
  import { tick, untrack } from "svelte";
  import { RoomStore } from "./lib/room.svelte.ts";
  import { ForwardExpiry } from "./lib/forward-clock.ts";
  import { replaceForward, sameForward, undoForward } from "./lib/forward-state.ts";
  import { blockedBy, call, retryDelay, type Blocked as BlockedState } from "./lib/client.ts";
  import Blocked from "./lib/ui/Blocked.svelte";
  import ConnectScreen from "./lib/ui/ConnectScreen.svelte";
  import Settings from "./lib/ui/Settings.svelte";
  import { linkCode } from "./lib/devices.ts";
  import Feed from "./lib/ui/Feed.svelte";
  import SessionThread from "./lib/ui/SessionThread.svelte";
  import StatusPill from "./lib/ui/StatusPill.svelte";
  import Composer from "./lib/ui/Composer.svelte";
  import WorkingLine from "./lib/ui/WorkingLine.svelte";
  import SessionRow from "./lib/ui/SessionRow.svelte";
  import AboutPanel from "./lib/ui/AboutPanel.svelte";
  import HostFoot from "./lib/ui/HostFoot.svelte";
  import VirtualList from "./lib/ui/VirtualList.svelte";
  import Icon from "./lib/ui/Icon.svelte";
  import Avatar, { setSessionLabels } from "./lib/ui/Avatar.svelte";
  import RoomIntro from "./lib/ui/RoomIntro.svelte";
  import { boxReason, count, quoted, turnReason, waits, type Quote } from "./lib/feed.ts";
  import { sessionTree, type TreeRow } from "./lib/session-tree.ts";
  import { FileDraft } from "./lib/attach.svelte.ts";
  import ComputerView from "./lib/ui/ComputerView.svelte";
  import type { Size } from "./lib/computerInput.ts";
  import JobsTool from "./lib/ui/JobsTool.svelte";
  import HeaderActions from "./lib/ui/HeaderActions.svelte";
  import TermDock from "./lib/ui/TermDock.svelte";
  import Home from "./lib/ui/Home.svelte";
  import Palette from "./lib/ui/Palette.svelte";
  import { remember, visits, type Item } from "./lib/palette.ts";
  import type { SearchCall } from "./lib/search.svelte.ts";
  import { MediaQuery, SvelteMap, SvelteSet } from "svelte/reactivity";
  import RoomMenu from "./lib/ui/RoomMenu.svelte";
  import RequestsPanel from "./lib/ui/RequestsPanel.svelte";
  import Instructions from "./lib/ui/Instructions.svelte";
  import NewSession from "./lib/ui/NewSession.svelte";
  import NewRoom from "./lib/ui/NewRoom.svelte";
  import ActiveNow from "./lib/ui/ActiveNow.svelte";
  import { activeRooms } from "./lib/active.ts";
  import { Attention } from "./lib/attention.svelte.ts";
  import { mentionable, parseMentions } from "./lib/mentions.ts";
  import { distinctInitials } from "./lib/identity.ts";
  import { computerPlace, fitted, type Rect } from "./lib/float.ts";
  import type { ComputerInput, Conversation, PostRequest } from "./lib/contract.gen.ts";
  // A pairing link leaves the address bar and history at once, whatever this browser's state. Only a signed-out page uses it.
  const move = new URLSearchParams(location.hash.slice(1));
  const moveData = move.has("route") ? move : new URLSearchParams(sessionStorage.getItem("dyna-connect-move") ?? "");
  let routeNotice = $state(moveData.get("route") ?? "");
  if (move.has("route")) sessionStorage.setItem("dyna-connect-move", move.toString());
  if (move.has("route")) history.replaceState(null, "", location.pathname + location.search);
  const pairCode = linkCode(window.location.hash);
  if (window.location.hash.includes("pair=")) history.replaceState(null, "", window.location.pathname + window.location.search);
  const formDrafts = new FormDrafts();
  function formEvent(event: FocusEvent | Event) {
    const control = event.target;
    if (control instanceof HTMLInputElement || control instanceof HTMLTextAreaElement) {
      if (event.type === "focusin") formDrafts.focus(control); else formDrafts.input(control);
    }
  }
  let firstBuild: string | undefined;
  let reloadPending = $state(false);
  let mainWidth = $state(4096);
  watchHello(({ build }) => {
    if (firstBuild === undefined) { firstBuild = build; return; }
    if (build === firstBuild || reloadPending) return;
    const focused = document.activeElement;
    const hasWork = Object.values(drafts).some(Boolean) || Object.values(quotes).some(Boolean) ||
      Object.values(unsent).some(list => list.length) || [...fileDrafts.values()].some(draft => draft.items.length > 0) ||
      formDrafts.pending || terminals?.hasInput() || ((focused instanceof HTMLInputElement || focused instanceof HTMLTextAreaElement) && focused.value);
    if (hasWork) reloadPending = true; else location.reload();
  });
  let streamUp: boolean | undefined;
  let reconnecting = $state(false);
  const reconnect = reconnectNotice(on => { reconnecting = on; });
  watchStreamState(up => { streamUp = up; reconnect(up); });
  void watchConnect().then(watch => { if (watch) { watchStreamState(watch.stream); if (streamUp !== undefined) watch.stream(streamUp); } }, () => {});
  const attention = new Attention();
  let rooms = $derived([...attention.rooms.values()]);
  // Distinct initials for every room avatar, so two rooms never look alike on the rail.
  let initials = $derived(distinctInitials(rooms.map(item => item.name)));
  // Home's Active now column, in the same place as a room's sessions panel. With no active room it leaves, and the page takes its width.
  let activity = $derived(activeRooms(attention));
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
  // Settings opens over the app, so the page behind it keeps its stream, its draft and its feed position.
  const isSettings = (where: string) => /^\/settings(\/|$)/.test(where);
  let behind = $state(isSettings(window.location.pathname) ? "/" : window.location.pathname);
  let inSettings = $derived(isSettings(path));
  function follow() {
    const previousRoom = (isSettings(path) ? behind : path).match(/^\/r\/([^/]+)/)?.[1];
    const nextRoom = window.location.pathname.match(/^\/r\/([^/]+)/)?.[1];
    if (nextRoom && nextRoom !== previousRoom) { routeNotice = ""; sessionStorage.removeItem("dyna-connect-move"); }
    path = window.location.pathname;
    if (inSettings) return;
    behind = path;
    section = /\/instructions\/?$/.test(path) ? "instructions" : /\/r\/[^/]+\/new\/?$/.test(path) ? "new-session" : /^\/rooms\/new\/?$/.test(path) ? "new-room" : undefined;
  }
  window.addEventListener("popstate", follow);
  function dismissRequests(event: MouseEvent) {
    if (section === "requests" && event.target instanceof Node && !page?.contains(event.target) && !requestsButton?.contains(event.target)) section = undefined;
  }
  // The requests view shows in place of the conversation until Back, Escape or the control that opened it.
  let section: "requests" | "instructions" | "new-session" | "new-room" | undefined = $state(/\/instructions\/?$/.test(behind) ? "instructions" : /\/r\/[^/]+\/new\/?$/.test(behind) ? "new-session" : /^\/rooms\/new\/?$/.test(behind) ? "new-room" : undefined);
  let requestsButton: HTMLButtonElement | undefined = $state();
  function openInstructions() {
    history.pushState({ instructionsBack: behind }, "", `/r/${encodeURIComponent(room!)}/instructions`);
    panel = false;
    follow();
  }
  function openNew(kind: "session" | "room") {
    history.pushState({ instructionsBack: behind }, "", kind === "room" ? "/rooms/new" : `/r/${encodeURIComponent(room!)}/new`);
    panel = false;
    follow();
  }
  async function closeSection(tapped = false) {
    if (section === "instructions" || section === "new-session" || section === "new-room") {
      if (history.state?.instructionsBack) history.back();
      else { history.replaceState(null, "", room ? `/r/${encodeURIComponent(room)}` : "/"); follow(); }
      return;
    }
    section = undefined;
    await tick();
    requestsButton?.focus({ focusVisible: !tapped });
  }
  // A link to a room the engine does not list, or to a path the app has no page for, shows a load error in Home's
  // place. Until the room list loads, a room link is trusted, so a known room opens without a flash.
  // Room, session and instructions paths are routes; other paths show no page.
  const route = $derived(/^\/r\/([^/]+)(?:\/s\/([^/]+)|\/instructions|\/new)?\/?$/.exec(behind));
  const asked = $derived(route?.[1] && decodeURIComponent(route[1]));
  let room = $derived(asked && (!roomsLoaded || attention.rooms.has(asked)) ? asked : undefined);
  let lost: "room" | "page" | undefined = $derived(room || behind === "/" || section === "new-room" ? undefined : asked ? "room" : "page");
  let session = $derived(room && route?.[2] ? decodeURIComponent(route[2]) : undefined);
  $effect(() => {
    if (!room) return;
    const key = "dyna.recent-places";
    localStorage.setItem(key, JSON.stringify(remember(visits(localStorage.getItem(key)), { room, session })));
  });
  // Derived, not set in the effect below: the room's views render with the store of the room they show.
  let store = $derived(room ? new RoomStore(room) : undefined);
  // The same rule per room for its open sessions.
  let sessionInitials = $derived(distinctInitials([...store?.names.values() ?? []]));
  setSessionLabels(() => sessionInitials);
  // Opens the shown room again; the effect below sets it for each room.
  let reopen = $state(() => {});
  // Before the template renders: a window waits for the room's lists, so the stream resumes after everything it read.
  $effect.pre(() => {
    // Home has no room, so no room controls or panels may show one.
    const current = store;
    if (!current) return;
    const opening = retrying(() => current.open().then(() => current.ready ? undefined : blockedBy(current.error)));
    reopen = opening.run;
    opening.run();
    return () => { opening.stop(); current.close(); };
  });
  // A room that failed to open is unavailable, for the same reasons as the page or its own; later load errors show inside the feed.
  let unavailable: BlockedState | "room" | undefined = $derived(!store || store.ready || store.error === undefined ? undefined : blockedBy(store.error) ?? "room");
  function navigate(event: MouseEvent & { currentTarget: HTMLAnchorElement }) {
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey) return;
    event.preventDefault();
    history.pushState(null, "", event.currentTarget.href);
    follow();
  }
  // A link to a DM closes a phone's panel. When wide, the link may leave with the list as About opens, and focus goes to the panel.
  function openSession(event: MouseEvent & { currentTarget: HTMLAnchorElement }) {
    navigate(event);
    if (!event.defaultPrevented) return;
    if (!wide.current) void showPanel(false, event.detail > 0);
    else void tick().then(() => { if (document.activeElement === document.body) members?.focus(); });
  }
  let home: Conversation = $derived(session ? { kind: "dm", session } : { kind: "feed" });
  // The sessions panel lists each child under its parent. Closed sessions leave it. As in v1, a session with
  // children folds its subtree, and its row counts the hidden sessions; folds last for this page view.
  // Siblings sort by the most urgent status in their subtree: what needs the owner, then working, then idle. Two or
  // more idle siblings fold into one "N idle" row that opens in place, so a room of 100 sessions shows its work first.
  const folded = new SvelteSet<string>();
  const openIdle = new SvelteSet<string>();
  const treeKey = (entry: TreeRow) => entry.kind === "idle" ? `idle ${entry.key}` : entry.session.session;
  let sessionList: VirtualList<TreeRow> | undefined = $state();
  const starting = (id: string) => { const box = store?.boxes.get(id); return box === "pending" || box === "creating" || box === "starting"; };
  let tree = $derived(sessionTree([...(store?.sessions.values() ?? [])], store?.statuses() ?? new Map(), starting, session, folded, openIdle));
  // Left folds a parent's subtree and Right opens it, on its row or its chevron.
  function fold(event: KeyboardEvent, id: string) {
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    if (event.key === "ArrowLeft") folded.add(id); else folded.delete(id);
  }
  // Drafts and failed sends belong to one conversation, keyed by room and session. A failed send shows in its
  // feed with Retry, also after the owner leaves and comes back; raw keeps each request's identity for onsettled.
  let drafts: Record<string, string> = $state({});
  // A draft that is not in memory yet comes from localStorage, so a reload keeps the text. Composer and send both read it this way.
  const savedDrafts = new ComposerDrafts(localStorage);
  let unsent: Record<string, PostRequest[]> = $state.raw({});
  const keyOf = (request: PostRequest) => `${request.room}/${request.home?.session ?? ""}`;
  const keep = (request: PostRequest) => { unsent = { ...unsent, [keyOf(request)]: [...unsent[keyOf(request)] ?? [], request] }; };
  const settle = (request: PostRequest) => { unsent = { ...unsent, [keyOf(request)]: (unsent[keyOf(request)] ?? []).filter(kept => kept !== request) }; };
  // A forwarded post waits in its conversation's composer like a draft.
  let quotes: Record<string, Quote | undefined> = $state({});
  let replaced: Record<string, Quote | undefined> = $state({});
  const expiry = new ForwardExpiry(conversation => { replaced[conversation] = undefined; });
  function clearReplacement() { expiry.clear(key); replaced[key] = undefined; }
  function forward(quote: Quote) {
    if (sameForward(quotes[key], quote)) { composer?.focus({ preventScroll: true }); return; }
    clearReplacement();
    const next = replaceForward({ quote: quotes[key] }, quote);
    if (next.previous) {
      replaced[key] = next.previous;
      expiry.start(key);
    }
    quotes[key] = next.quote;
    composer?.focus({ preventScroll: true });
  }
  // Each conversation keeps its own draft files; FileDraft is reactive inside, so a plain map holds them.
  const fileDrafts = new Map<string, FileDraft>();
  const files = (key: string) => { let draft = fileDrafts.get(key); if (!draft) fileDrafts.set(key, draft = new FileDraft()); return draft; };
  let key = $derived(`${room}/${session ?? ""}`);
  // Pausing stops new turns in the room; attention keeps the state current from the stream.
  let paused = $derived(attention.rooms.get(room ?? "")?.paused ?? false);
  // The room whose last pause or resume failed, so another room keeps its own line.
  let pauseFailed: string | undefined = $state();
  function pause() {
    pauseFailed = undefined;
    const target = room;
    if (target) void call("room.pause", { room: target, paused: !paused }).catch(() => { pauseFailed = target; });
  }
  // A DM's running turn, which the composer can stop.
  let stop = $derived.by(() => {
    const running = session, current = store;
    if (!running || !current?.turns.has(running)) return undefined;
    return { stopping: current.stopping.has(running), failed: current.stopFailed.has(running), run: () => current.stop(running) };
  });
  function send() {
    if (!store || !room) return;
    const text = savedDrafts.draft(key, drafts[key]), quote = quotes[key], draft = files(key);
    if (draft.busy || (!text.trim() && !quote && !draft.items.length)) return;
    const attachments = [...quote?.attachments ?? [], ...draft.take()];
    drafts[key] = "";
    savedDrafts.clear(key);
    quotes[key] = undefined;
    clearReplacement();
    // The engine refuses mentions on a DM post; in the feed, @names of open sessions become mentions.
    // Mentions come from the owner's own words only, so an @name inside a forwarded post wakes nobody.
    const post = session ? { text, mentions: [] } : parseMentions(text, store.sessions.values());
    // A forward carries the post's files; the engine keeps each file once, by hash, for every conversation.
    const request: PostRequest = { room, text: quote ? quoted(quote, post.text) : post.text, mentions: post.mentions.length ? post.mentions : undefined,
      home: session ? home : undefined, attachments: attachments.length ? attachments : undefined };
    if (session) void thread?.send(request); else void view?.send(request);
  }
  let view: Feed | undefined = $state();
  let thread: SessionThread | undefined = $state();
  let rail: HTMLElement | undefined = $state();
  let feed: HTMLDivElement | undefined = $state();
  let page: HTMLElement | undefined = $state();
  let composer: HTMLTextAreaElement | undefined = $state();
  let members: HTMLElement | undefined = $state();
  let computer: HTMLElement | undefined = $state();
  let terminals: TermDock | undefined = $state();
  let jobsTool: JobsTool | undefined = $state();
  let roomMenu: RoomMenu | undefined = $state();
  let settings: HTMLElement | undefined = $state();
  let settingsMenu: HTMLButtonElement | undefined = $state();
  let dock: HTMLElement | undefined = $state();
  let palette = $state(false);
  let paletteOpener: HTMLElement | undefined;
  let jumpSeq: number | undefined = $state();
  const searchPosts: SearchCall = request => call("post.search", request);
  function closePalette() {
    palette = false;
    void tick().then(() => (paletteOpener?.isConnected ? paletteOpener : composer)?.focus());
  }
  function paletteOpen(item: Item) {
    closePalette();
    if (item.kind === "more") return;
    if (item.kind === "action") {
      if (item.action.id === "new-session") openNew("session");
      else if (item.action.id === "new-room") openNew("room");
      else if (item.action.id === "pause") pause();
      else if (item.action.id === "requests") { if (inSettings) { history.pushState(null, "", behind); follow(); } section = "requests"; }
      else { history.pushState(null, "", item.action.id === "theme" ? "/settings/appearance" : "/settings"); follow(); }
      return;
    }
    const targetRoom = item.kind === "room" ? item.room.id : item.kind === "session" ? item.session.room : item.post.room;
    const targetSession = item.kind === "session" ? item.session.id : item.kind === "message" && item.post.home.kind === "dm" ? item.post.home.session : undefined;
    const seq = item.kind === "message" ? item.post.seq : undefined;
    openLocation(targetRoom, targetSession, seq);
  }
  function openLocation(targetRoom: string, targetSession?: string, seq?: number) {
    jumpSeq = undefined;
    history.pushState(null, "", `/r/${encodeURIComponent(targetRoom)}${targetSession ? `/s/${encodeURIComponent(targetSession)}` : ""}`);
    follow();
    if (seq !== undefined) void tick().then(() => { jumpSeq = seq; });
  }
  // F6 (mod+F6 in a browser) moves focus between areas; Shift goes back.
  let goKey = 0;
  function cycle(event: KeyboardEvent) {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      if (!palette) { paletteOpener = document.activeElement instanceof HTMLElement ? document.activeElement : undefined; palette = true; }
      return;
    }
    if (palette) return;
    if ((event.ctrlKey || event.metaKey) && event.key === ",") {
      event.preventDefault();
      history.pushState(null, "", "/settings");
      follow();
      return;
    }
    const editing = event.target instanceof HTMLElement && (event.target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(event.target.tagName));
    if (!editing && !event.ctrlKey && !event.metaKey && !event.altKey) {
      if (event.key.toLowerCase() === "r" && goKey && event.timeStamp - goKey < 1000 && room) {
        event.preventDefault();
        if (inSettings) { history.pushState(null, "", behind); follow(); }
        section = "requests";
      }
      goKey = event.key.toLowerCase() === "g" ? event.timeStamp : 0;
    }
    if (event.key === "Escape" && drawer) void showDrawer(false);
    else if (event.key === "Escape" && panel && !wide.current) void showPanel(false);
    else if (event.key === "Escape" && section && !inSettings) void closeSection();
    if (event.key !== "F6") return;
    event.preventDefault();
    if (covered) return;
    // Only areas on screen take part; below 761 px the closed sessions panel has no boxes.
    // The requests view takes the place of the feed and the composer.
    const areas = (inSettings ? [rail, settings] : [rail, section ? page : feed, section ? undefined : composer, dock, members, docked ? undefined : computer])
      .filter(area => area != null)
      .filter(area => area.getClientRects().length > 0);
    const index = areas.findIndex(area => area.contains(document.activeElement));
    areas[(index + (event.shiftKey ? areas.length - 1 : 1)) % areas.length].focus();
  }
  // Up and Down move between the sessions in the tree and its idle folds; Enter opens one.
  // The tree is virtual, so the next row may not be rendered yet: the list scrolls to it first. A held key's presses
  // that come meanwhile move on from that row, and only the newest one focuses.
  let walkingTo: number | undefined;
  // Focus goes back to a row's computer icon when the docked computer closes. The list may have scrolled, so
  // the row is scrolled back in first; and a row shows its icons only on hover or focus, so its link takes focus
  // before the icon. With the icon gone, focus stays on the link.
  async function focusRowIcon(id: string, tapped = false) {
    const index = tree.findIndex(entry => entry.kind === "session" && entry.session.session === id);
    if (index >= 0) await sessionList?.reveal(index);
    const link = members?.querySelector<HTMLElement>(`a.member[data-key="${CSS.escape(id)}"]`);
    link?.focus({ focusVisible: !tapped });
    (members?.querySelector<HTMLElement>(`[data-watch="${CSS.escape(id)}"]`) ?? link ?? members)?.focus({ focusVisible: !tapped });
  }
  async function walkTree(event: KeyboardEvent & { currentTarget: HTMLElement }) {
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" || !members) return;
    event.preventDefault();
    const index = walkingTo ?? tree.findIndex(entry => treeKey(entry) === event.currentTarget.dataset.key);
    const next = Math.max(0, Math.min(tree.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)));
    // The tree can change while the list scrolls, so the row is found again by its key, never by its old index.
    const key = treeKey(tree[next]);
    const target = () => members?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`);
    walkingTo = next;
    if (!target()) await sessionList?.reveal(next);
    if (walkingTo !== next) return;
    walkingTo = undefined;
    target()?.focus();
  }
  let roomName = $derived(attention.rooms.get(room ?? "")?.name ?? "Room");
  // A session's line says what it does, from its one status (status.ts). A waiting one names what the owner must do;
  // a box no host takes for its room comes first, since its requests show in the feed and Requests too. A failed
  // session's line is the reason after "Failed", which the row draws; an idle one says why its pending box waits, or
  // Starting while its box starts, else nothing. Its profile is the row's hover title.
  const status = (id: string) => {
    const broken = store?.boxFailures.get(id), cause = store?.causes.get(id), reason = store?.pendingReasons.get(id);
    switch (store?.status(id)) {
      case "waiting": return reason === "room-host" ? waits[reason] : "Waiting for your approval";
      case "working": { const label = store?.activity.get(id); return label ? `Working · ${label}` : "Working"; }
      case "failed": return broken ? boxReason(broken) : turnReason(cause);
      default: return reason ? waits[reason] : starting(id) ? "Starting" : "";
    }
  };
  // A DM's header says only the status word; the live line and About say what the session does. A failed session has
  // no live line, so the header gives its reason.
  const stateWords = { waiting: "Waiting", failed: "Failed", working: "Working" };
  let shown = $derived(session ? store?.status(session) ?? "idle" : "idle");
  let word = $derived(session ? (shown === "idle" ? status(session) : shown === "failed" ? `Failed · ${status(session)}` : stateWords[shown]) : "");
  // The right panel opens on About in a DM and on Sessions in a room, and remembers the last choice per conversation.
  const tabs = new SvelteMap<string, "sessions" | "about">();
  // Below 761 px the sessions panel is hidden; a header button opens it over the feed.
  // Opening moves focus into the panel; closing returns it to the button if the panel held it.
  let panel = $state(false);
  // From 761 px the panel shows until the owner closes it.
  let tucked = $state(false);
  // The session whose computer the owner watches, while it runs: in a window over the app, or docked
  // in the sessions panel instead of the list. Docked, the view mounts only while the panel shows.
  // Each session's window keeps its place and size for the page view.
  let watching: string | undefined = $state();
  let docked = $state(false);
  let rect = $state({ x: window.innerWidth - 504, y: window.innerHeight - 420, w: 480, h: 300 });
  // A session's first window opens inside the feed's right edge, clear of its cards and the composer; where it
  // cannot clear them it docks, unless the owner has used Dock or Undock. It then opens where the owner left it.
  // On a phone the window fills the screen (fix 3).
  const places = new Map<string, Rect>();
  let dockChosen = false;
  // The header's Computer button when it opened the view; focus goes back to it, as the row's icon has no place under About.
  let opener: HTMLElement | undefined;
  const centered = (box: DOMRect) => (box.left + box.right + 744) / 2;
  function watch(id: string, from?: HTMLElement) {
    if (watching) places.set(watching, rect);
    const box = (section ? page : feed)?.getBoundingClientRect();
    const place = places.get(id);
    if (place) rect = place;
    else if (box) {
      // Feed cards start at the feed's left; the Requests page centers its 744 px cards.
      const first = computerPlace(box.right, box.top + 16, section ? centered(box) : box.left + 680);
      rect = first.rect;
      if (!dockChosen && !phone.current) {
        docked = !first.clear;
        if (docked && !panelShown) void showPanel(true).then(() => computer?.focus());
      }
    }
    opener = from;
    watching = id;
  }
  // Requests opened under a floating window docks it if it would hide the cards; a window the owner moved clear stays,
  // and focus stays on the page, whose keys decide requests.
  $effect(() => {
    if (!section || !page || !watching || docked || phone.current) return;
    const box = page.getBoundingClientRect();
    untrack(() => {
      if (dockChosen || rect.x >= centered(box) + 8) return;
      docked = true;
      void (panelShown ? tick() : showPanel(true)).then(() => page?.focus());
    });
  });
  // The view closes when its computer stops or the room changes, before the DOM updates, so it never
  // streams again unasked. Focus that was in the view returns to the row's button, or to the panel;
  // below 761 px with the panel closed, it returns to the panel's toggle button.
  async function stopWatching(tapped = false) {
    const id = watching ?? "";
    if (watching) places.set(watching, rect);
    const held = members?.contains(document.activeElement) || computer?.contains(document.activeElement);
    watching = undefined;
    await tick();
    if (!held) return;
    if (opener?.isConnected) opener.focus({ focusVisible: !tapped });
    else if (!members?.getClientRects().length) { if (toggle?.isConnected) toggle.focus({ focusVisible: !tapped }); else roomMenu?.focus({ focusVisible: !tapped }); }
    else await focusRowIcon(id, tapped);
  }
  // Every refusal shows in the view as a notice; its next batch goes on.
  const sendInput = (session: string, events: ComputerInput[], frame: Size) => call("computer.input", { session, frame, events }).then(() => undefined);
  $effect.pre(() => { if (watching && !store?.computers.has(watching)) void stopWatching(); });
  // A session's running background jobs while its box is ready. The header's Jobs tool, like About's Jobs row, keeps every
  // session that ever had jobs, since a finished job's output stays in its box. In a DM it lists only that session.
  const jobs = (id: string) => store?.boxes.get(id) === "ready" ? store.shellJobs.get(id) ?? 0 : 0;
  const wide = new MediaQuery("min-width: 761px");
  let panelShown = $derived(wide.current ? !tucked : panel);
  // Below 641 px the rail is a drawer, as in Discord's phone app: the rooms on the left and the open room's pages beside
  // them. A pick closes it; the scrim and Escape close it and return focus to the menu button. A keyboard open or
  // close (Escape) shows the ring; a tap open focuses the dialog itself, and no ring follows a tap either way.
  const phone = new MediaQuery("max-width: 640px");
  // On a phone the full-screen computer covers everything else, so nothing behind it takes focus.
  let covered = $derived(phone.current && !!watching && !docked && !inSettings && section !== "instructions");
  let drawer = $state(false);
  let menuButton: HTMLButtonElement | undefined = $state();
  async function showDrawer(open: boolean, refocus = true, tapped = false) {
    drawer = open;
    await tick();
    if (open && tapped) rail?.focus({ focusVisible: false });
    else if (open) (rail?.querySelector<HTMLElement>(".pages [aria-current=page]") ?? rail?.querySelector<HTMLElement>(".pages a"))?.focus();
    else if (refocus) (inSettings ? settingsMenu : menuButton)?.focus({ focusVisible: !tapped });
  }
  $effect(() => { if (!phone.current) drawer = false; });
  // A room's open sheet closes on the way Home, and Home's own closes when its last active room leaves.
  $effect(() => { if (!room) panel = false; });
  // Before the DOM update, so showPanel still sees focus in the sheet that is about to leave.
  $effect.pre(() => { if (!room && !activity.active.length && panel) { if (wide.current) panel = false; else void showPanel(false); } });
  // On a phone the sessions panel is a bottom sheet that fits its content, up to about two thirds of the height (fix 1). Its handle drags it to
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
    if (wide.current) tucked = !open; else panel = open;
    if (!open) sheetFull = false;
    await tick();
    // Home's strip leaves with its last active room, and then the menu button takes focus.
    if (open) members?.focus({ focusVisible: !tapped }); else if (held) { if (toggle?.isConnected) toggle.focus({ focusVisible: !tapped }); else if (session) roomMenu?.focus({ focusVisible: !tapped }); else menuButton?.focus({ focusVisible: !tapped }); }
  }
  // The sessions the feed composer offers after @: the root first, then by name. A DM offers none.
  let mentions = $derived(session ? [] : [...store?.sessions.values() ?? []].filter(mentionable)
    .map(item => { const state = store?.status(item.session);
      return { name: item.name, root: !item.parent, status: state === "idle" ? "" : state === "failed" ? `Failed · ${status(item.session)}` : status(item.session) }; })
    .sort((a, b) => Number(b.root) - Number(a.root) || a.name.localeCompare(b.name)));
  // The panel's header counts, and the room header's below 761 px where the panel is hidden.
  // One count source (status.ts): a session that waits for the owner is waiting, not working.
  let working = $derived(store?.counts.working ?? 0);
  let title = $derived(lost ? "Not found" : !room ? "Home" : session ? store?.names.get(session) ?? "Session" : roomName);
  // Each fact shows once: profile, state and trouble live on the session's row. A closed session has no row.
  let closed = $derived(!!session && !!store?.ready && !store.sessions.has(session));
  // A closed session keeps its history to read, with no composer. One with no history at all never existed.
  let missing = $derived(closed && !!view?.empty());
  let about = $derived(!!session && !closed && (tabs.get(key) ?? "about") === "about");
  // room.list gives the feed's read position. Opening a session's DM shows the owner its failure, which clears the mark.
  // This device was revoked while its page stayed open.
  let revoked = $state(false);
  let signedOut = $derived(revoked || blocked === "signed-out" || unavailable === "signed-out");
  // Inside Settings the drawer lists rooms, as its room pages act on a view that Settings covers.
  let pagesRoom = $derived(inSettings ? undefined : room);
  $effect(() => { const read = attention.rooms.get(room ?? "")?.lastRead; if (store && read) store.noteRead({ kind: "feed" }, read); });
  // Only opening counts: a failure that arrives while the DM is open clears once its row is on screen.
  $effect(() => {
    const open = session, current = store;
    if (open && current?.ready) untrack(() => { if (current.unseen(open)) current.see({ kind: "dm", session: open }, current.failed.get(open) ?? 0); });
  });
  // The tab names the page only. Counts stay out: a count in the title would act as an unread badge (spec 5.13).
  // An open Instructions or Requests section names the tab too. Settings names its own open pane, so this effect leaves Settings alone.
  let tabTitle = $derived(section === "instructions" ? `${title} · Instructions` : section === "requests" ? `${title} · Requests` : title);
  $effect(() => { if (!inSettings) document.title = `${tabTitle} · Dyna`; });
</script>


{#snippet intro(history: boolean)}
  <!-- A DM with history needs only the one muted line. -->
  {#if session}<RoomIntro kind="session" compact={history} name={title} root={!!store?.sessions.get(session) && !store.sessions.get(session)?.parent} line={`This is the start of your direct messages with ${title}.`} />
  {:else}{@const created = attention.rooms.get(room ?? "")?.created}
    <RoomIntro kind="room" name={roomName} label={initials.get(roomName)} line={`This is the beginning of #${roomName}.${created ? ` It started ${new Date(created).toLocaleDateString([], { month: "long", day: "numeric", year: "numeric" })}.` : ""}`} />{/if}
{/snippet}

{#snippet computerView(id: string)}
  <!-- Another session's view is a new view, so the old image runs its cleanup and drops its stream. -->
  {#key id}<ComputerView session={id} name={store?.names.get(id) ?? "Session"} root={!store?.sessions.get(id)?.parent} activity={store?.activity.get(id)} steps={store?.steps.get(id) ?? []} {docked} full={phone.current && !docked} bind:rect bind:element={computer} input={attention.can("computers") ? (events, frame) => sendInput(id, events, frame) : undefined}
    onfit={(ratio, chrome) => { if (!places.has(id)) places.set(id, rect = fitted(rect, ratio, chrome, (feed?.getBoundingClientRect().bottom ?? window.innerHeight) - 16)); }}
    onclose={tapped => void stopWatching(tapped)} ondock={event => { docked = !docked; dockChosen = true; if (docked && !panelShown) void showPanel(true, event.detail > 0); }} />{/key}
{/snippet}

<svelte:window onkeydown={cycle} onclickcapture={dismissRequests} onfocusin={formEvent} oninput={formEvent} onpagehide={() => savedDrafts.flush()} />
{#if palette}<Palette {rooms} {room} search={searchPosts} onclose={closePalette} onopen={paletteOpen} />{/if}
{#snippet sheetHandle(of: string)}
  <button type="button" class="handle" aria-label={sheetFull ? `Shrink the ${of} sheet` : `Expand the ${of} sheet`}
    onpointerdown={drag} onclick={() => { if (!dragged) sheetFull = !sheetFull; dragged = false; }}></button>
{/snippet}

{#if signedOut}<ConnectScreen code={pairCode} />
{:else}<div class="shell" class:open={room || activity.active.length} class:drawer class:tucked={room && wide.current && tucked} class:settings={inSettings}>
  <aside class="rail" aria-label="Rooms" tabindex="-1" bind:this={rail} inert={phone.current && !drawer} role={drawer ? "dialog" : undefined} aria-modal={drawer || undefined}
    onclick={event => { if (drawer && event.target instanceof Element && event.target.closest("a, button")) void showDrawer(false, false); }}>
    <nav aria-label="Rooms"><a class="home" href="/" onclick={navigate} aria-current={room || inSettings ? undefined : "page"} aria-label="Home" title="Home"><Icon name="home" /></a>
      <!-- A room with requests, or with sessions whose box waits for a host that takes the room, waits for the owner and
           gets an amber dot on its corner, with no number (spec 5.13). A room with a failed session gets a red one
           instead, in the priority order of status.ts. -->
      {#each rooms as item (item.id)}{@const waiting = attention.waiting.get(item.id)?.size ?? 0}{@const hostless = attention.hostless.get(item.id)?.size ?? 0}{@const failing = attention.failing(item.id)}
      <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate} aria-current={room === item.id && !inSettings ? "page" : undefined}
        aria-label={[item.name, failing && `${count(failing, "session")} failed`, waiting && `${count(waiting, "request")} waiting`, hostless && `${count(hostless, "session")} waiting for a host`].filter(Boolean).join(", ")} title={item.name}>
        <Avatar kind="room" name={item.name} label={initials.get(item.name)} size={40} />{#if failing}<StatusPill tone="failed" label="Failed" dot />{:else if waiting || hostless}<StatusPill tone="warning" label="Waiting" dot />{/if}</a>
    {/each}<button type="button" class="tool" title="Create a room" aria-label="Create a room" onclick={() => openNew("room")}><Icon name="plus" /></button></nav>
    <footer><a class="gear" href="/settings" onclick={navigate} aria-current={inSettings ? "page" : undefined} aria-label="Settings" title="Settings"><Icon name="gear" size={20} /></a></footer>
    <!-- The drawer's second column: the open room's pages, or on Home every room by name. Counts are muted; only Requests
         uses the warning tone. -->
    {#if phone.current}<nav class="pages" aria-label={pagesRoom ? `${roomName} pages` : "Rooms by name"}><strong>{pagesRoom ? roomName : "Home"}</strong>
      {#if pagesRoom}
        <a href={"/r/" + encodeURIComponent(pagesRoom)} onclick={navigate} aria-current={!session && !section ? "page" : undefined}><Icon name="chat" />Chat</a>
        {#if store && !unavailable}
          <button type="button" aria-pressed={section === "requests"} onclick={() => { section = "requests"; }}><Icon name="inbox" />Requests
            {#if store.waiting}<StatusPill tone="warning" label="Waiting" count={store.waiting} />{/if}</button>
          <button type="button" onclick={event => void showPanel(true, event.detail > 0)}><Icon name="people" />Sessions<span class="count">{store.sessions.size}</span></button>
          {#if attention.can("admin")}<button type="button" aria-pressed={section === "instructions"} onclick={openInstructions}><Icon name="file" />Instructions</button>{/if}
          {#if attention.can("operate")}<button type="button" onclick={pause}><Icon name="pause" />{paused ? "Resume room" : "Pause room"}</button>{/if}
        {/if}
      {:else}
        {#each rooms as item (item.id)}{@const waiting = attention.waiting.get(item.id)?.size ?? 0}
          <a href={"/r/" + encodeURIComponent(item.id)} onclick={navigate}><Avatar kind="room" name={item.name} label={initials.get(item.name)} size={28} />{item.name}
            {#if waiting}<StatusPill tone="warning" label="Waiting" count={waiting} />{/if}</a>
        {/each}
      {/if}</nav>{/if}
  </aside>
  {#if drawer || (panel && phone.current)}<button type="button" class="scrim" tabindex="-1" aria-label={drawer ? "Close the menu" : room ? "Close the sessions" : "Close the active rooms"}
    onclick={() => void (drawer ? showDrawer(false, true, true) : showPanel(false, true))}></button>{/if}
  <main bind:clientWidth={mainWidth} class:header-tight={mainWidth <= 1000} class:header-narrow={mainWidth <= 600} inert={drawer || (panel && phone.current) || covered || inSettings}>
    {#if (section === "new-room" || (!room && roomsLoaded && !rooms.length && !blocked))}<NewRoom {rooms} first={!rooms.length} onclose={() => void closeSection()} oncreated={id => { void attention.open().then(() => openLocation(id)); }} />
    {:else if section === "new-session" && room && store && store.ready}<NewSession {room} {roomName} sessions={[...store.sessions.values()]} onclose={() => void closeSection()} onstarted={id => openLocation(room!, id)} />
    {:else if section === "instructions" && room && store && !unavailable}<Instructions {room} {roomName} rooms={rooms.length} sessions={store.sessions.size} onclose={() => void closeSection()} />
    {:else}
    <header class="bar" class:session-bar={!!session}>{#if room && session && phone.current}<a class="menu-toggle" href={"/r/" + encodeURIComponent(room)} aria-label={`Back to ${roomName}`} title={`Back to ${roomName}`}
        onclick={event => { const tapped = event.detail > 0; navigate(event); void tick().then(() => menuButton?.focus({ focusVisible: !tapped })); }}><Icon name="back" /></a>
      {:else}<button type="button" class="menu-toggle" bind:this={menuButton} aria-label="Rooms and pages" title="Rooms and pages" aria-expanded={drawer}
        onclick={event => void showDrawer(true, true, event.detail > 0)}><Icon name="menu" /></button>{/if}
      {#if room && session}<a class="crumb" href={"/r/" + encodeURIComponent(room)} onclick={navigate}>{roomName}</a><span class="count">/</span>
      {:else if room}<span class="hash"><Icon name="room" /></span>{/if}
      {#if session && !closed}<Avatar kind="session" name={title} root={!store?.sessions.get(session)?.parent} size={24} />{/if}
      <span class="heading"><strong>{title}</strong>
        {#if closed && !missing}<span class="count">closed</span>{:else if !session && word && !missing}<span class="count word">{word}</span>{/if}
        {#if store?.ready && !session}<span class="count narrow">{count(store.sessions.size, "session")}</span>{/if}</span>
      {#if store && !unavailable}
        {#if session}<span class="session-spacer"></span>{/if}
        {#if session && !closed && attention.can("terminal") && store.boxes.get(session) === "ready"}<button type="button" class="tool" aria-label="Terminal" title={terminals?.hasShell(session) ? "Show this session's terminal" : "Open a terminal in this session's box"} aria-pressed={terminals?.hasShell(session) ?? false} onclick={() => terminals?.shell(session, title, !store?.sessions.get(session)?.parent)}><Icon name="terminal" /></button>{/if}
        {#if session && attention.can("computers") && store.computers.has(session)}<button type="button" class="tool" aria-label="Computer" title="Watch this session's computer" onclick={event => watch(session, event.currentTarget)}><Icon name="monitor" /></button>{/if}
        {#key session}<HeaderActions {store} {session} {attention} {closed} phone={phone.current} {paused} panelShown={panelShown} requestsOpen={section === "requests"}
          bind:jobsTool bind:roomMenu bind:requestsButton bind:toggle
          onopen={(item, job) => terminals?.openJob(item, job)} onpause={pause} oninstructions={openInstructions} onnewsession={() => openNew("session")} onrequests={() => { section = section === "requests" ? undefined : "requests"; }} onpanel={tapped => void showPanel(!panelShown, tapped)}
          onterminal={() => terminals?.shell(session!, title, !store?.sessions.get(session!)?.parent)}
          oncomputer={() => watch(session!)} />{/key}
      {/if}</header>
    {#if routeNotice}<div class="route-notice"><Icon name="globe" size={18} /><span><strong>Moved to {routeNotice}</strong>{moveData.has("old") ? `: ${moveData.get("old")} stopped answering for 20 seconds.` : `, a nearer route to ${moveData.get("name") || "this machine"}. Same session, same room.`}</span><button type="button" aria-label="Dismiss route notice" onclick={() => { routeNotice = ""; sessionStorage.removeItem("dyna-connect-move"); }}>✕</button></div>{/if}
    {#if reloadPending}<p class="reload-notice" role="status">Dyna updated. Reload to use the new version. <button type="button" onclick={() => location.reload()}>Reload</button></p>{/if}
    {#if reconnecting}<p class="reconnect-notice" role="status">Reconnecting to Dyna…</p>{/if}
    {#if section === "requests" && store && !unavailable}<RequestsPanel bind:element={page} {store} {session} admin={attention.can("admin")} onclose={() => void closeSection()} />{/if}
    <div class="feed">
      {#if blocked}<Blocked kind="unreachable" onretry={connect} />
      {:else if !room && roomsLoaded && !rooms.length}<Blocked kind="no-rooms" />
      {:else if lost === "room"}<Blocked kind="no-room" back={{ href: "/", label: "Go to Home" }} {navigate} />
      {:else if lost === "page"}<Blocked kind="no-page" back={{ href: "/", label: "Go to Home" }} {navigate} />
      {:else if !room}<Home {attention} {initials} loaded={roomsLoaded} {navigate} onrequests={() => { section = "requests"; }} active={activity.active}
        onactive={event => { toggle = event.currentTarget; void showPanel(true, event.detail > 0); }} />
      {:else if unavailable === "unreachable"}<Blocked kind="unreachable" onretry={reopen} />
      {:else if store && !unavailable}
        {#if session}{#key session}<SessionThread bind:this={thread} {store} {session} {roomName} {navigate} unsent={unsent[key] ?? []} onunsent={keep} onsettled={settle} onpostlocation={(room, seq) => openLocation(room, undefined, seq)} />{/key}{:else}{#key key}<Feed bind:this={view} {store} {home} admin={attention.can("admin")} operate={attention.can("operate") && !closed} waiting={quotes[key]} target={jumpSeq} ontarget={() => jumpSeq = undefined} bind:element={feed} {intro} unsent={unsent[key] ?? []} onunsent={keep} onsettled={settle}
          onforward={forward} onclosed={() => composer?.focus()} onjobs={item => void jobsTool?.show(item)}
          />{/key}{/if}
        <!-- The empty feed stays mounted behind the error, as it is how the page learns the session has no history. -->
        {#if missing && room}<Blocked kind="no-session" back={{ href: "/r/" + encodeURIComponent(room), label: `Back to ${roomName}` }} {navigate} />{/if}
      {:else}<p class="empty">This room is not available. <button type="button" class="retry" onclick={reopen}>Try again</button></p>{/if}
    </div>
    {#if room && !unavailable && !closed}
      <!-- A paused room's line takes the working line's place, so the composer never moves; the header still says who works. -->
      {#if paused || pauseFailed === room}<p class="paused" role="status"><span>{pauseFailed === room ? "The room did not change." : "Room paused. No session starts a new turn."}</span>
        {#if attention.can("operate")}<button type="button" class="retry" onclick={pause}>{pauseFailed === room ? "Try again" : "Resume"}</button>{/if}</p>
      {:else if store && !session}<WorkingLine {store} {home} />{/if}
      {#if attention.can("operate")}<Composer bind:element={composer} bind:value={() => savedDrafts.draft(key, drafts[key]),text => { drafts[key] = text; savedDrafts.save(key, text); clearReplacement(); }} onsend={send} thread={!!session} placeholder={"Message " + title}
        quote={quotes[key]} replacement={replaced[key]} onundo={() => { quotes[key] = undoForward({ quote: quotes[key], previous: replaced[key] }).quote; clearReplacement(); }} onunquote={() => { quotes[key] = undefined; clearReplacement(); }} sessions={mentions} files={files(key)} {stop} />
      {:else}<p class="readonly" role="status">This device can read the room but not post in it.</p>{/if}
    {/if}
    {/if}
    <div class="terminal-holder" hidden={section === "instructions"}>
      <TermDock bind:this={terminals} terminal={attention.can("terminal")} bind:element={dock} onempty={() => composer?.focus()} phone={phone.current} strip={!!room && !session && !unavailable}
        sessions={[...store?.sessions.values() ?? []].filter(item => store?.boxes.get(item.session) === "ready").map(item => ({ id: item.session, name: item.name, root: !item.parent }))} />
    </div>
  </main>
  {#if room}
    <aside class="members" id="sessions" class:shown={panel} class:full={sheetFull} class:watching={watching && docked} aria-label="Sessions" tabindex="-1" role={panel && phone.current && !covered ? "dialog" : undefined} aria-modal={(panel && phone.current && !covered) || undefined} bind:this={members} inert={drawer || covered || inSettings}>
      <!-- Docked, the computer sits above the session list. -->
      {#if watching && docked && panelShown}{@render computerView(watching)}{/if}
      {#if phone.current}{@render sheetHandle("sessions")}{/if}
      {#if session && !closed}<header class="bar tabs">
        <button type="button" aria-pressed={!about} onclick={() => tabs.set(key, "sessions")}>Sessions</button>
        <button type="button" aria-pressed={about} onclick={() => tabs.set(key, "about")}>About</button></header>
      {:else}<header class="bar"><strong>Sessions</strong>{#if store?.ready}<span class="count">{store.sessions.size + (working ? ` · ${working} working` : "")}</span>{/if}{#if attention.can("operate")}<button type="button" class="tool" title="New session" aria-label="New session" onclick={() => openNew("session")}><Icon name="plus" /></button>{/if}</header>{/if}
      <!-- A long tree renders only its visible rows, through the feed's shared list (spec: long lists are virtual). -->
      {#if about && session && store}<AboutPanel {store} room={room} {session} switchable={attention.can("operate")} navigate={openSession} onjobs={async event => { if (!wide.current) await showPanel(false, event.detail > 0); void jobsTool?.show(session); }} />
      {:else if tree.length}<div class="tree"><VirtualList bind:this={sessionList} items={tree} key={treeKey}
        label="Session list" role="group" align="top" estimate={48}>
        {#snippet row(entry: TreeRow)}
        {#if entry.kind === "idle"}<button type="button" class="idle" data-key={treeKey(entry)} aria-expanded={entry.open} style:padding-left="{entry.depth * 14}px" onkeydown={walkTree}
          onclick={() => { if (!openIdle.delete(entry.key)) openIdle.add(entry.key); }}><Icon name="chevron" size={12} />{count(entry.count, "idle session")}</button>
        {:else}{@const { session: item, depth, parent, hidden, state, dot, mark } = entry}
        <div class="row">{#if parent}<button type="button" class="fold" aria-expanded={!hidden} style:left="{6 + depth * 14}px"
          aria-label={hidden ? `Show ${count(hidden, "session")} under ${item.name}` : `Hide the sessions under ${item.name}`}
          title={hidden ? `${count(hidden, "session")} folded` : "Fold"} onclick={() => { if (!folded.delete(item.session)) folded.add(item.session); }}
          onkeydown={event => fold(event, item.session)}><Icon name="chevron" size={12} />{#if hidden}<span>{hidden}</span>{/if}</button>{/if}
        <!-- Reserve about 7 px per hidden-count digit beside the chevron. -->
        <a class="member" data-key={item.session} href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(item.session)}`}
          onclick={openSession}
          onkeydown={event => { void walkTree(event); if (parent) fold(event, item.session); }}
          aria-current={session === item.session ? "page" : undefined} title={item.profile} style:padding-left="{34 + depth * 14 + (hidden ? String(hidden).length * 7 : 0)}px">
          <SessionRow name={item.name} root={!item.parent} {state} {dot} line={status(item.session)} {mark} /></a>
          {#if jobs(item.session)}<button type="button" class="watch" aria-label={`${item.name}'s background jobs, ${jobs(item.session)} running`}
            title={`${item.name}'s background jobs, ${jobs(item.session)} running`} onclick={async event => { if (!wide.current) await showPanel(false, event.detail > 0); void jobsTool?.show(item.session); }}><Icon name="terminal" /></button>{/if}
          {#if attention.can("computers") && store?.computers.has(item.session)}<button type="button" class="watch" class:open={watching === item.session} data-watch={item.session} aria-label={`Watch ${item.name}'s computer`}
            title={`Watch ${item.name}'s computer`} onclick={() => watch(item.session)}><Icon name="monitor" /></button>{/if}</div>{/if}
        {/snippet}</VirtualList></div>
      {:else}<p class="hint">No sessions yet.</p>{/if}
      <!-- The foot polls hosts, so it mounts only while the panel shows; a phone's closed panel polls nothing. -->
      {#if panelShown}<HostFoot {navigate} />{/if}
    </aside>
    {#if watching && !docked && !inSettings && section !== "instructions"}{@render computerView(watching)}{/if}
  {:else if activity.active.length}
    <aside class="members" id="active" class:shown={panel} class:full={sheetFull} aria-label="Active now" tabindex="-1" role={panel && phone.current ? "dialog" : undefined} aria-modal={(panel && phone.current) || undefined} bind:this={members} inert={drawer || inSettings}>
      {#if phone.current}{@render sheetHandle("active rooms")}{/if}
      <header class="bar"><strong>Active now</strong><span class="count active">{activity.active.length}</span></header>
      <div class="tree rooms"><ActiveNow {...activity} {initials} navigate={openSession} /></div>
    </aside>
  {/if}
  {#if inSettings}<Settings {path} phone={phone.current} {navigate} onmenu={event => void showDrawer(true, true, event.detail > 0)} signedout={() => { revoked = true; }} bind:element={settings} bind:menu={settingsMenu} />{/if}
</div>{/if}

<style>
  .route-notice { display: flex; align-items: center; gap: 10px; padding: 8px 16px; color: var(--muted); font-size: 12.5px; border-bottom: 1px solid var(--line); }
  .route-notice span { flex: 1; } .route-notice strong { color: var(--text); }
  .route-notice button { border: 0; background: none; color: inherit; cursor: pointer; }
  .reconnect-notice { margin: 0; padding: 7px 16px; color: var(--muted); border-bottom: 1px solid var(--line); font-size: 13px; }
  .reload-notice { margin: 0; padding: 7px 16px; color: var(--muted); border-bottom: 1px solid var(--line); font-size: 13px; }
  .reload-notice button { margin-left: 4px; padding: 8px 4px; border: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
  /* Two-line rows around the 28 px avatar, as in the chosen mockup: the name, then what the session does. */
  a.member { min-height: 48px; color: var(--text); text-decoration: none; }
  /* The panel's header matches the room header: a 16 px title with its muted counts beside it. */
  aside.members > header strong, .count.active { font-size: 16px; }
  /* The header's face rings its dot in the header's color. */
  main > header { --ring: var(--bg); }
  .feed:has(> :global(.blocked)) > :global(:not(.blocked)) { display: none; }
  .tabs { gap: 4px; }
  .tabs button { padding: 4px 10px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .tabs button:hover { background: var(--hover); color: var(--text); }
  .tabs button[aria-pressed="true"] { background: var(--selected); color: var(--text); }
  .session-spacer { flex: 1; }
  .session-bar .tool { border: 0; padding: 0 7px; color: var(--muted); }
  .tool { display: flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px; border: 1px solid var(--line); border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer; }
  .tool:hover { background: var(--hover); }
  .tool[aria-pressed="true"] { background: var(--selected); }
  /* The session's name gets the crumb's width: the rail or a phone's back arrow leads to the room. */
  @media (max-width: 760px) { .crumb, .crumb + .count { display: none; } }
  @media (max-width: 760px) { main .tool { justify-content: center; width: 40px; height: 40px; padding: 0; border: 0; } }
  main { position: relative; }
  .terminal-holder:not([hidden]) { display: contents; }
  :where(main.header-tight) .tool { justify-content: center; width: 28px; padding: 0; }
  /* One quiet line of the working line's size and edge; only its Resume acts. */
  .paused { flex: none; display: flex; gap: 6px; height: 20px; margin: 0 0 4px; padding: 0 16px 0 30px; color: var(--muted); font-size: 12.5px; line-height: 20px; white-space: nowrap; }
  .paused span { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .paused button { flex: none; }
  .readonly { flex: none; margin: 0; padding: 12px 16px 14px 30px; color: var(--muted); font-size: 12.5px; }
  .shell.open.tucked, .shell.open:has(:global(.instructions)) { grid-template-columns: 64px 1fr; }
  .shell.open:has(:global(.instructions)) aside.members { display: none; }
  @media (max-width: 640px) { .shell.open:has(:global(.instructions)) { grid-template-columns: minmax(0, 1fr); } }
  .shell.tucked > aside.members { display: none; }
  .narrow, .menu-toggle { display: none; }
  @media (max-width: 760px) { .narrow { display: inline; } }
  .heading { display: contents; }
  /* The header has one line, and a failed session's word is a sentence, so the word gives way and the title stays whole. */
  .word { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .heading strong { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @media (min-width: 641px) { .word { flex: 1 1 0; } }
  .crumb { flex: none; max-width: 25%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .hash { display: inline-flex; align-items: center; }
  /* A phone's header: the menu button, the title over its muted counts, Requests only while something waits (40 px
     targets), and the Sessions button with its count. The room menu's one action, Terminal, moves into the drawer. */
  @media (max-width: 640px) {
    .menu-toggle { display: grid; place-items: center; flex: none; width: 40px; height: 40px; margin-left: -10px; padding: 0; border: 0; border-radius: 8px;
      background: none; color: var(--muted); text-decoration: none; cursor: pointer; }
    /* Touch screens keep :hover after a tap, so the wash waits for a real pointer. */
    @media (hover: hover) { .menu-toggle:hover { background: var(--hover); color: var(--text); } }
    .hash { display: none; }
    .session-spacer { display: none; }
    .session-bar .tool { display: none; }
    .heading { display: flex; flex: 1; flex-direction: column; min-width: 0; line-height: 1.25; }
    .heading .count { font-size: 12.5px; }
    /* The header keeps its width for the room's name: the faces and the count already say who works. */
    .scrim { position: fixed; inset: 0; z-index: 2; padding: 0; border: 0; background: var(--scrim); }
  }
  .handle { display: none; }
  @media (max-width: 640px) {
    aside.members:not(.full) .tree { flex: 0 1 auto; display: flex; flex-direction: column; }
    .handle { all: unset; position: relative; display: block; width: 100%; height: 22px; flex: none; cursor: grab; touch-action: none; }
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
  .rooms { padding: 8px; }
  .row { position: relative; display: flex; align-items: center; }
  /* The chevron sits in the gutter left of the avatar. As in v1, a folded row's hidden count is part of the same
     button, but it sits on the row line beside the chevron. */
  .fold { all: unset; position: absolute; z-index: 1; top: 50%; transform: translateY(-50%); min-width: 16px; min-height: 24px; display: flex; align-items: center; justify-content: center;
    padding: 0 1px; border-radius: 4px; color: var(--muted); font-size: 11px; line-height: 1.1; font-variant-numeric: tabular-nums; cursor: pointer; }
  @media (hover: hover) { .fold:hover { color: var(--text); } }
  .fold:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .fold[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  .row a.member { flex: 1; min-width: 0; }
  /* "N idle sessions" stands for idle siblings until opened. Its chevron sits in the fold gutter and its words line
     up with the names. */
  .idle { all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 12px; width: 100%; height: 32px; color: var(--muted);
    font-size: 12.5px; cursor: pointer; }
  .idle :global(svg) { flex: none; width: 16px; }
  .idle[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  @media (hover: hover) { .idle:hover { background: var(--hover); color: var(--text); } }
  .idle:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .watch { all: unset; display: grid; place-items: center; width: 24px; height: 24px; margin-right: 8px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  @media (hover: hover) { .watch:hover { background: var(--hover); color: var(--text); } }
  .watch:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  /* A thumb gets v1's taller target, so a near miss does not open the DM. This follows the base rules it overrides. */
  @media (hover: none) { .fold { width: 24px; min-height: 44px; } .idle { height: 44px; } .watch { width: 40px; height: 40px; margin-right: 0; } }
  /* The name wins: a pointer shows a row's icons on hover, and the keyboard on focus; an open view keeps its icon. */
  @media (hover: hover) { .row:not(:hover, :focus-within) .watch:not(.open) { display: none; } }
  /* The docked computer needs 420 px. It docks beside the feed from 1024 px, where the feed keeps 540 px; below
     that it covers the feed, and Back or Escape closes it. */
  @media (min-width: 1024px) { .shell.open:not(.tucked):has(aside.watching) { grid-template-columns: 64px 1fr 420px; } }
  @media (min-width: 761px) and (max-width: 1023px) { .shell > aside.members.watching { position: fixed; top: calc(var(--panel-top, 52px) + env(safe-area-inset-top)); right: 0; bottom: 0; width: 420px; z-index: 1; box-shadow: -8px 0 24px var(--shadow-lg); } }
  @media (max-width: 760px) { .shell > aside.members.shown.watching { width: min(420px, 100vw); } }
</style>
