<script lang="ts">
  import { onMount, tick, untrack } from "svelte";
  import { MediaQuery } from "svelte/reactivity";
  import { call } from "../client.ts";
  import type { RoomSummary } from "../contract.gen.ts";
  import { build, rows, marks, count, showAll, move, parse, escape, backspace, stamp, type Action, visits, type Item, type Person, type Place, type Scope, type Visit } from "../palette.ts";
  import { MessageSearch, SessionSearch, type SearchCall } from "../search.svelte.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import { hue } from "../identity.ts";

  let { rooms, room, search, onclose, onopen }: {
    rooms: RoomSummary[]; room?: string; search: SearchCall; onclose: () => void; onopen: (item: Item) => void;
  } = $props();
  const phone = new MediaQuery("max-width: 640px");
  let input: HTMLInputElement;
  let list: HTMLDivElement;
  let dialog: HTMLElement;
  let text = $state(""), scope: Scope | undefined = $state(), local = $state(false), pick = $state(0);
  let people: Person[] = $state.raw([]);
  let loadedRooms: Set<string> = $state.raw(new Set());
  let recent: Visit[] = $state.raw([]);
  let retry = $state(0);
  const messages = new MessageSearch(untrack(() => search));
  const sessionSearch = new SessionSearch(request => call("session.search", request));
  let current = $derived(rooms.find(found => found.id === room));
  let actions: Action[] = $derived([
    ...current ? [
      { id: "new-session", label: "New session", hint: `in ${current.name}`, keys: [], icon: "plus" },
      { id: "requests", label: "Requests", hint: current.waiting?.length ? `${current.waiting.length} wait${phone.current ? "" : " for you"}` : "", keys: ["G", "R"], icon: "inbox" },
      { id: "pause", label: current.paused ? "Resume the room" : "Pause the room", hint: `${current.name} · ${current.paused ? "Sessions can start turns again" : "No session starts a new turn"}`, keys: [], icon: "pause" },
    ] satisfies Action[] : [],
    { id: "new-room", label: "Create a room", hint: "", keys: [], icon: "plus" },
    { id: "theme", label: "Theme", hint: "Carbon, Ink, Nord, Paper, Light or OLED black", keys: [], icon: "palette" },
    { id: "settings", label: "Settings", hint: "", keys: ["Ctrl", ","], icon: "gear" },
  ]);
  let places: Place[] = $derived(rooms.map(found => ({ id: found.id, name: found.name, paused: found.paused,
    sessions: loadedRooms.has(found.id) ? people.filter(person => person.room === found.id && !person.closed).length : undefined,
    working: found.working?.length ?? 0 })));
  let mode = $derived(parse(text, scope));
  let recentItems: Item[] = $derived(recent.flatMap((visit): Item[] => {
    if (visit.session) {
      const person = people.find(found => found.id === visit.session && found.room === visit.room);
      return person ? [{ kind: "session", session: person }] : [];
    }
    const place = places.find(found => found.id === visit.room);
    return place ? [{ kind: "room", room: place }] : [];
  }));
  let sessionMatches = $derived(sessionSearch.snapshot);
  let searchingSessions = $derived(!!mode.query && (!mode.scope || mode.scope === "sessions"));
  let foundPeople: Person[] = $derived(sessionMatches.sessions.map(found => {
    const open = people.find(person => person.id === found.id);
    return { id: found.id, name: found.name, room: found.room, roomName: rooms.find(room => room.id === found.room)?.name ?? "Room",
      root: open?.root ?? false, working: open?.working ?? false, closed: found.closed };
  }));
  let sections = $derived(build(text, scope, { rooms: places, sessions: searchingSessions ? foundPeople : people,
    sessionsTotal: searchingSessions ? sessionMatches.total : undefined,
    sessionsState: searchingSessions && (sessionMatches.state === "searching" || sessionMatches.state === "failed") ? sessionMatches.state : undefined,
    actions, recent: recentItems, messages: messages.snapshot }));
  let items = $derived(rows(sections));

  onMount(() => {
    input.focus();
    recent = visits(localStorage.getItem("dyna.recent-places"));
    for (const found of rooms) {
      void call("session.list", { room: found.id }).then(result => {
        people = [...people, ...(result.sessions ?? []).map(person => ({ id: person.id, name: person.name, room: found.id,
          roomName: found.name, root: !person.parent, closed: false, working: !!person.turn }))];
        loadedRooms = new Set([...loadedRooms, found.id]);
      }, () => {});
    }
  });
  $effect(() => {
    const query = mode.query, selected = mode.scope, target = local ? room : undefined;
    void retry;
    const term = selected && selected !== "messages" ? "" : query;
    messages.prepare(term);
    const timer = setTimeout(() => void messages.start(term, target), 150);
    return () => clearTimeout(timer);
  });
  $effect(() => {
    const term = searchingSessions ? mode.query : "";
    void retry;
    sessionSearch.prepare(term);
    const timer = setTimeout(() => void sessionSearch.start(term), 150);
    return () => clearTimeout(timer);
  });
  $effect(() => { void text; void scope; pick = 0; });
  function choose(item: Item) {
    if (item.kind === "more") { scope = item.scope; pick = 0; void tick().then(() => input.focus()); return; }
    onopen(item);
  }
  function keys(event: KeyboardEvent) {
    if (event.key === "Tab") {
      const controls = [...dialog.querySelectorAll<HTMLElement>('input, button:not([disabled]):not([tabindex="-1"])')].filter(control => control.getClientRects().length > 0);
      const index = controls.findIndex(control => control === document.activeElement);
      if ((event.shiftKey && index <= 0) || (!event.shiftKey && index === controls.length - 1)) {
        event.preventDefault();
        controls[event.shiftKey ? controls.length - 1 : 0]?.focus();
      }
    } else if (event.key === "Escape") {
      event.preventDefault(); event.stopPropagation();
      const result = escape({ scope, text });
      if (result.close) onclose(); else { scope = result.mode.scope; text = result.mode.text; }
    } else if (event.key === "Backspace") {
      const result = backspace({ scope, text });
      if (result) { event.preventDefault(); scope = result.scope; }
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault(); pick = move(pick, items.length, event.key);
      void tick().then(() => list.querySelector<HTMLElement>('[aria-selected="true"]')?.scrollIntoView({ block: "nearest" }));
    } else if (event.key === "Enter" && items[pick]) {
      event.preventDefault(); choose(items[pick]);
    }
  }
  function scrolled() {
    if (scope === "messages" && list.scrollTop + list.clientHeight >= list.scrollHeight - 80) void messages.more();
  }
  const name = (id?: string) => (id && messages.names[id]) || people.find(person => person.id === id)?.name || "Session";
  const roomName = (id: string) => rooms.find(found => found.id === id)?.name ?? "Room";
</script>

<div class="shade" role="presentation" onclick={event => { if (event.target === event.currentTarget) onclose(); }}>
  <div class="palette" role="dialog" aria-modal="true" aria-label="Search and actions" tabindex="-1" bind:this={dialog} onkeydown={keys}>
    <div class="input"><Icon name="search" size={20} />
      {#if scope}<button class="chip" onclick={() => { scope = undefined; input.focus(); }}>{scope} <Icon name="close" size={12} /></button>{/if}
      <input bind:this={input} bind:value={text} placeholder={phone.current ? "Search everything" : "Search rooms, sessions, messages and actions"} aria-label="Search rooms, sessions, messages and actions"
        role="combobox" aria-expanded="true" aria-controls="palette-list" aria-activedescendant={items[pick] ? `palette-${pick}` : undefined} autocomplete="off" />
      {#if mode.query && mode.scope !== "actions" && room}<button class="scope" onclick={() => local = !local}>{local ? roomName(room) : "All rooms"} <Icon name="chevron" size={12} /></button>{/if}
      <button class="cancel" onclick={onclose}>Cancel</button><kbd class="esc">Esc</kbd>
    </div>
    <div class="list" id="palette-list" role="listbox" bind:this={list} onscroll={scrolled}>
      {#each sections as section}
        <div class="group">{#if scope === "messages" && section.group === "messages"}{section.total.toLocaleString()} messages<span>· newest first</span>{:else}{section.title} {#if count(section)}<span>{count(section)}</span>{/if}{/if}</div>
        {#each section.items as item}
          {@const index = items.indexOf(item)}
          <button type="button" id="palette-{index}" role="option" aria-selected={pick === index} tabindex="-1" class="row" class:chosen={pick === index}
            class:message={item.kind === "message"} class:action={item.kind === "action"} class:request={item.kind === "action" && item.action.id === "requests"} class:more={item.kind === "more"} onmousemove={() => pick = index} onclick={() => choose(item)}>
            {#if item.kind === "room"}
              <Avatar kind="room" name={item.room.name} size={22} />
              <span class="name">{#each marks(item.room.name, mode.query, Infinity) as part}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</span>
              {#if item.room.paused || item.room.sessions !== undefined}<span class="sub">{item.room.paused ? "Paused" : ""}{item.room.paused && item.room.sessions !== undefined ? " · " : ""}{item.room.sessions !== undefined ? `${item.room.sessions} ${item.room.sessions === 1 ? "session" : "sessions"}` : ""}</span>{/if}
            {:else if item.kind === "session"}
              <Avatar kind="session" name={item.session.name} root={item.session.root} size={22} />
              <span class="name">{#each marks(item.session.name, mode.query, Infinity) as part}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</span>
              <span class="sub">{item.session.closed ? "Closed · " : item.session.working ? "Working · " : ""}{item.session.roomName}</span>
            {:else if item.kind === "action"}
              <span class="icon"><Icon name={item.action.icon} /></span><span class="name">{#each marks(item.action.label, mode.query, Infinity) as part}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</span>
              <span class="sub">{item.action.hint}</span><span class="keys">{#each item.action.keys as key}<kbd>{key}</kbd>{/each}</span>
            {:else if item.kind === "more"}
              <span class="icon"></span><span>{showAll(item.scope, item.total)}</span>
            {:else}
              {@const author = item.post.author.kind === "owner" ? "You" : item.post.author.kind === "engine" ? "Dyna" : name(item.post.author.session)}
              <Avatar kind={item.post.author.kind} name={author} size={22} />
              <span class="body"><span class="excerpt">{#each marks(item.post.text, mode.query) as part}{#if part.hit}<mark>{part.text}</mark>{:else}{part.text}{/if}{/each}</span>
                <span class="meta"><strong style:--hue={hue(author)}>{author}</strong><span>in #{roomName(item.post.room)}</span><span>·</span><time>{stamp(item.post.time)}</time></span></span>
            {/if}
          </button>
        {/each}
        {#if scope === "sessions" && section.group === "sessions" && section.shown < section.total}<p class="note">Showing {section.shown.toLocaleString()} of {section.total.toLocaleString()} sessions. Narrow the search to see the rest.</p>{/if}
        {#if section.state === "searching"}<div class="note">Searching {section.group}…<div class="skeleton"></div><div class="skeleton short"></div></div>
        {:else if section.state === "failed"}<div class="note">Could not search {section.group}. <button class="retry" onclick={() => retry++}>Retry</button></div>{/if}
      {/each}
      {#if mode.query && !sections.length}<p class="note">No room, session, message or action matches “{mode.query}”.</p>{/if}
    </div>
    <footer><span><kbd>↑</kbd> <kbd>↓</kbd> move</span><span><kbd>Enter</kbd> open</span><span><kbd>&gt;</kbd> actions only</span><span class="fact">{scope ? "Esc goes back to all results" : "Matches every word by its start"}</span></footer>
  </div>
</div>

<style>
  .shade { position: fixed; inset: 0; z-index: 100; display: flex; justify-content: center; align-items: flex-start; padding: 96px 16px 16px; background: var(--scrim); }
  .palette { width: min(680px, 100%); max-height: min(720px, calc(100dvh - 112px)); display: flex; flex-direction: column; background: var(--raised); border: 1px solid var(--line); border-radius: 16px; box-shadow: 0 24px 64px var(--shadow-lg); overflow: hidden; }
  .input { min-height: 56px; display: flex; gap: 10px; align-items: center; padding: 0 16px; border-bottom: 1px solid var(--line); color: var(--muted); }
  input { flex: 1; min-width: 0; border: 0; background: none; color: var(--text); font: inherit; font-size: 16px; outline: none; }
  .input:focus-within { box-shadow: inset 0 0 0 2px var(--focus); }
  input::placeholder { color: var(--muted); }
  button { font: inherit; cursor: pointer; }
  .scope, .chip, .cancel { border: 0; background: none; color: var(--muted); white-space: nowrap; }
  .chip { text-transform: capitalize; background: var(--selected); border-radius: 6px; padding: 4px 8px; display: flex; gap: 4px; align-items: center; }
  .scope { display: flex; align-items: center; gap: 4px; border: 1px solid var(--line); border-radius: 20px; padding: 4px 8px; }
  .cancel { display: none; }
  kbd { font: inherit; font-size: 11px; color: var(--muted); background: var(--selected); border: 1px solid var(--line); border-radius: 4px; padding: 2px 5px; }
  .list { overflow: auto; padding: 8px; min-height: 0; }
  .group { padding: 12px 10px 6px; font-size: 13px; font-weight: 600; color: var(--muted); }
  .group span { color: var(--muted); font-weight: 400; margin-left: 6px; }
  .row { width: 100%; display: flex; align-items: center; gap: 10px; min-height: 38px; padding: 6px 10px; border: 0; border-radius: 6px; background: none; color: var(--text); text-align: left; }
  .row.chosen { background: var(--selected); }
  .name { flex: 0 1 auto; min-width: 0; overflow-wrap: anywhere; font-weight: 500; }
  .sub { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: 12.5px; }
  .icon { width: 22px; flex: none; display: grid; place-items: center; color: var(--muted); }
  .keys { margin-left: auto; display: flex; gap: 4px; }
  .message { align-items: flex-start; }
  .more { color: var(--focus); }
  .body { min-width: 0; display: grid; gap: 4px; }
  .excerpt { display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; line-height: 1.4; }
  .meta { display: flex; gap: 7px; color: var(--muted); font-size: 11px; }
  .meta strong { color: oklch(var(--id-initials) var(--hue)); }
  mark { color: var(--text); background: color-mix(in srgb, var(--attention) 25%, transparent); border-radius: 3px; }
  .note { margin: 0; padding: 10px; color: var(--muted); font-size: 13px; }
  .retry { color: var(--focus); background: none; border: 0; }
  .skeleton { height: 8px; width: 70%; margin-top: 10px; border-radius: 4px; background: var(--selected); }
  .skeleton.short { width: 52%; }
  footer { display: flex; gap: 16px; padding: 10px 16px; border-top: 1px solid var(--line); color: var(--muted); font-size: 11px; }
  .fact { margin-left: auto; }
  @media (max-width: 640px) {
    .shade { padding: 0; }
    .palette { width: 100%; height: 100dvh; max-height: none; border: 0; border-radius: 0; }
    .list { flex: 1; }
    .row { min-height: 44px; }
    .action:not(.request) .sub { display: none; }
    .cancel { display: block; }
    .esc, footer, .keys { display: none; }
  }
</style>
