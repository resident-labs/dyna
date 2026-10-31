<script lang="ts">
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { call } from "../client.ts";
  import type { Attention } from "../attention.svelte.ts";
  import type { AccountUsage, HostSummary, Principal, SessionListResponse } from "../contract.gen.ts";
  import { hostMeters } from "../hosts.ts";
  import { hue } from "../identity.ts";
  import Avatar from "./Avatar.svelte";
  import HostCard from "./HostCard.svelte";
  import UsageCard from "./UsageCard.svelte";
  type Navigate = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  // onrequests opens the Requests panel once the link has moved to that room.
  let { attention, navigate, onrequests }: { attention: Attention; navigate: Navigate; onrequests: () => void } = $props();
  // Each room's sessions load once while Home is open, for Activity's counts and names. Requests load per room, and
  // again only when a room gains a request or requester that no answer named yet. A failed load forgets its IDs and
  // offers Try again.
  const requests = new SvelteMap<string, { title: string; by: Principal }>();
  const sessions = new SvelteMap<string, { name: string; root: boolean }>();
  // Requester IDs a session.list answered for; one it did not name is a closed session.
  const answered = new SvelteSet<string>();
  // Rooms whose session.list answered.
  const listed = new SvelteSet<string>();
  const asked = new Set<string>();
  let failed = $state(false);
  let attempt = $state(0);
  function load<T>(ids: Iterable<string>, known: { has(id: string): boolean }, list: () => Promise<T>, keep: (answer: T, fresh: string[]) => void) {
    const fresh = [...ids].filter(id => !known.has(id) && !asked.has(id));
    if (!fresh.length) return;
    for (const id of fresh) asked.add(id);
    void list().then(answer => keep(answer, fresh), () => { for (const id of fresh) asked.delete(id); failed = true; });
  }
  const keepSessions = (room: string) => (list: SessionListResponse, fresh: string[]) => {
    for (const item of list.sessions ?? []) sessions.set(item.id, { name: item.name, root: !item.parent });
    for (const id of fresh) answered.add(id);
    listed.add(room);
  };
  $effect(() => {
    void attempt;
    for (const room of attention.rooms.keys()) {
      load([room], listed, () => attention.listSessions(room), keepSessions(room));
      load(attention.waiting.get(room) ?? [], requests, () => call("op.list", { room }), list => {
        for (const op of list.operations ?? []) requests.set(op.id, { title: op.card.title, by: op.by });
      });
      // A requester that started after the room's list loads it again.
      const by = [...attention.waiting.get(room) ?? []].flatMap(id => requests.get(id)?.by.session ?? []);
      if (listed.has(room)) load(by, sessions, () => attention.listSessions(room), keepSessions(room));
    }
  });
  const rows = <T,>(sets: Map<string, Set<string>>, known: SvelteMap<string, T>) => [...attention.rooms.values()].flatMap(room =>
    [...sets.get(room.id) ?? []].flatMap(id => { const value = known.get(id); return value === undefined ? [] : [{ id, room, value }]; }));
  // A request shows once its requester is named, as in the Requests panel.
  let waiting = $derived(rows(attention.waiting, requests).flatMap(row => {
    const { by } = row.value;
    if (by.kind !== "session" || !by.session) return [{ ...row, who: by.kind === "owner" ? "You" : "Dyna", root: false }];
    const session = sessions.get(by.session);
    if (!session && !answered.has(by.session)) return [];
    return [{ ...row, who: session?.name ?? "A closed session", root: session?.root ?? false }];
  }));
  // More than 4 requests fold into one row per room, which opens that room's Requests.
  let groups = $derived(waiting.length <= 4 ? [] : [...attention.rooms.values()].flatMap(room => {
    const count = waiting.filter(row => row.room.id === room.id).length;
    return count ? [{ room, count }] : [];
  }));
  // Activity sums per room, never one row per session: each room with sessions, busiest first, six at most until the
  // owner asks for the rest. A failed session shows in its room, on its row and the rail.
  let busy = $derived([...attention.rooms.values()].flatMap(room => {
    const working = attention.working.get(room.id)?.size ?? 0, idle = Math.max(0, (attention.sessions.get(room.id)?.size ?? 0) - working);
    return working || idle ? [{ room, line: [working && `${working} working`, idle && `${idle} idle`].filter(Boolean).join(" · "), working }] : [];
  }).sort((a, b) => b.working - a.working || a.room.name.localeCompare(b.room.name)));
  let allRooms = $state(false);
  // A list shows while the store has items, as in v1's Home a failed load is never "all clear";
  // its count is the rows shown.
  const total = (sets: Map<string, Set<string>>) => [...sets.values()].reduce((sum, set) => sum + set.size, 0);
  let missing = $derived(waiting.length < total(attention.waiting)
    || [...attention.rooms.keys()].some(room => !listed.has(room)));
  const roomHref = (room: string) => "/r/" + encodeURIComponent(room);
  // Hosts load when Home opens, then every 10 s to match the engine's beat, only while the page is visible.
  // A failed call keeps the last answer, and the next tick tries again. A tick skips while a call is out, so
  // calls never pile up on a slow engine and answers arrive in order.
  let hosts: HostSummary[] | undefined = $state();
  let hostsFailed = $state(false);
  let hostsAt = $state(0);
  $effect(() => {
    let timer: ReturnType<typeof setInterval> | undefined;
    let loading = false;
    const tick = () => {
      if (loading) return;
      loading = true;
      // A control plane that reports no load shows no card, never an empty one.
      void call("host.list", {}).then(list => { hosts = (list.hosts ?? []).filter(h => h.name !== "control-plane" || hostMeters(h).length); hostsAt = Date.now(); hostsFailed = false; }, () => { hostsFailed = true; })
        .finally(() => { loading = false; });
    };
    const sync = () => { clearInterval(timer); if (document.hidden) return; tick(); timer = setInterval(tick, 10_000); };
    sync();
    document.addEventListener("visibilitychange", sync);
    return () => { clearInterval(timer); document.removeEventListener("visibilitychange", sync); };
  });
  // Model usage loads once when Home opens: the engine caches it for 5 min. Reset times count from that answer.
  let accounts: AccountUsage[] | undefined = $state();
  let accountsFailed = $state(false);
  let loadedAt = $state(0);
  $effect(() => {
    void call("accounts.quota", {}).then(list => { accounts = list.accounts ?? []; loadedAt = Date.now(); }, () => { accountsFailed = true; });
  });
</script>

<div class="home">
  <!-- Waiting for you, then Activity, each a full-width card whose rows fill a grid of about 280 px columns. -->
  <div class="top">
    <section class="card"><h2>Waiting for you{#if waiting.length}<span>{waiting.length}</span>{/if}</h2>
      <div class="rows">{#each groups as { room, count } (room.id)}<a class="item" href={roomHref(room.id)} onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
        <Avatar kind="room" name={room.name} size={28} />
        <span class="lines"><span class="title room" style:--hue={hue(room.name)}>{room.name}</span><span class="line">{count} waiting</span></span></a>
      {:else}{#each waiting as { id, room, value, who, root } (id)}<a class="item" href={roomHref(room.id)}
        onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
        <Avatar kind={value.by.kind} name={who} {root} size={28} />
        <span class="lines"><span class="title">{value.title}</span><span class="line">{who} · {room.name}</span></span></a>{/each}{/each}</div>
      {#if !total(attention.waiting)}<p class="none">Nothing is waiting for you.</p>{/if}</section>
    <section class="card"><h2>Activity{#if total(attention.working)}<span>{total(attention.working)} working</span>{/if}</h2>
      <div class="rows">{#each allRooms ? busy : busy.slice(0, 6) as { room, line } (room.id)}<a class="item" href={roomHref(room.id)} onclick={navigate}>
        <Avatar kind="room" name={room.name} size={28} />
        <span class="lines"><span class="title room" style:--hue={hue(room.name)}>{room.name}</span><span class="line">{line}</span></span></a>{/each}</div>
      {#if busy.length > 6 && !allRooms}<button type="button" class="retry more" onclick={() => { allRooms = true; }}>{busy.length - 6} more {busy.length === 7 ? "room" : "rooms"}</button>{/if}
      {#if !busy.length}<p class="none">No session is open.</p>{/if}</section>
  </div>
  {#if failed && missing}<p class="failed">Some items could not load. <button type="button" class="retry" onclick={() => { failed = false; attempt++; }}>Try again</button></p>{/if}
  {#if hosts?.length || hostsFailed}<h2>Machines{#if hosts?.length}<span>{hosts.length}</span>{/if}</h2>{/if}
  {#if !hosts && hostsFailed}<p class="failed">Machines could not load.</p>{/if}
  {#if hosts?.length}<div class="cards">{#each hosts as host (host.name)}<HostCard {host} now={hostsAt} />{/each}</div>{/if}
  {#if accounts?.length || accountsFailed}<h2>Usage{#if accounts?.length}<span>{accounts.length}</span>{/if}</h2>{/if}
  {#if accountsFailed}<p class="failed">Model usage could not load.</p>{/if}
  {#if accounts?.length}<div class="cards">{#each accounts as account, i (i)}<UsageCard {account} now={loadedAt} />{/each}</div>{/if}
</div>

<style>
  /* Home is a dashboard, so its card grids use the page width up to 1200 px. */
  .home { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; width: min(1200px, 100%); margin: 0 auto; padding: 16px 16px 32px; }
  /* 340 px columns keep three metric tiles' figures on one line. */
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(340px, 100%), 1fr)); gap: 12px; margin: 0 8px; }
  .top { display: grid; gap: 12px; margin: 8px 8px 0; }
  .rows { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(280px, 100%), 1fr)); column-gap: 12px; margin: 0 -8px; }
  h2 { display: flex; align-items: baseline; gap: 8px; margin: 24px 8px 12px; font-size: 18px; font-weight: 600; line-height: 1.25; }
  .card h2 { margin: 0 0 8px; font-size: 16px; }
  h2 span { color: var(--muted); font-weight: 400; font-variant-numeric: tabular-nums; }
  .item { display: flex; align-items: center; gap: 10px; min-width: 0; min-height: 44px; padding: 6px 8px; border-radius: 8px; color: var(--text); text-decoration: none; }
  .item:hover { background: var(--hover); }
  .none { margin: 12px 0 4px; color: var(--muted); font-size: 13px; }
  .failed { margin: 12px 8px; color: var(--muted); font-size: 13px; }
  /* A request row has SessionRow's shape: a main line, then one muted line that ends with the room. */
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title, .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-weight: 600; font-size: 14px; }
  .line { color: var(--muted); font-size: 12px; }
  .room { color: oklch(var(--id-name) var(--hue)); }
  .more { display: block; margin: 8px 0 4px; color: var(--muted); font-size: 13px; }
</style>
