<script lang="ts">
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { call } from "../client.ts";
  import type { Attention } from "../attention.svelte.ts";
  import type { AccountUsage, HostSummary, Principal, SessionListResponse } from "../contract.gen.ts";
  import { hostMeters } from "../hosts.ts";
  import Avatar from "./Avatar.svelte";
  import HostCard from "./HostCard.svelte";
  import SessionRow from "./SessionRow.svelte";
  import UsageCard from "./UsageCard.svelte";
  type Navigate = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  // onrequests opens the Requests panel once the link has moved to that room.
  let { attention, navigate, onrequests }: { attention: Attention; navigate: Navigate; onrequests: () => void } = $props();
  // Requests and sessions load per room while Home is open, and again only when a room gains an ID
  // that no answer named yet. A failed load forgets its IDs and offers Try again.
  const requests = new SvelteMap<string, { title: string; by: Principal }>();
  const sessions = new SvelteMap<string, { name: string; root: boolean }>();
  // Requester IDs a session.list answered for; one it did not name is a closed session.
  const answered = new SvelteSet<string>();
  const asked = new Set<string>();
  let failed = $state(false);
  let attempt = $state(0);
  function load<T>(ids: Iterable<string>, known: SvelteMap<string, unknown>, list: () => Promise<T>, keep: (answer: T, fresh: string[]) => void) {
    const fresh = [...ids].filter(id => !known.has(id) && !asked.has(id));
    if (!fresh.length) return;
    for (const id of fresh) asked.add(id);
    void list().then(answer => keep(answer, fresh), () => { for (const id of fresh) asked.delete(id); failed = true; });
  }
  const keepSessions = (list: SessionListResponse, fresh: string[]) => {
    for (const item of list.sessions ?? []) sessions.set(item.id, { name: item.name, root: !item.parent });
    for (const id of fresh) answered.add(id);
    attention.seed(list.sessions ?? []);
  };
  $effect(() => {
    void attempt;
    for (const room of attention.rooms.keys()) {
      load(attention.waiting.get(room) ?? [], requests, () => call("op.list", { room }), list => {
        for (const op of list.operations ?? []) requests.set(op.id, { title: op.card.title, by: op.by });
      });
      // Requesters load beside the requests, so Try again reaches them too.
      const by = [...attention.waiting.get(room) ?? []].flatMap(id => requests.get(id)?.by.session ?? []);
      load(by, sessions, () => call("session.list", { room }), keepSessions);
      load(attention.working.get(room) ?? [], sessions, () => call("session.list", { room }), keepSessions);
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
  let working = $derived(rows(attention.working, sessions));
  // A list shows while the store has items, as in v1's Home a failed load is never "all clear";
  // its count is the rows shown.
  const some = (sets: Map<string, Set<string>>) => [...sets.values()].some(set => set.size > 0);
  let anyWaiting = $derived(some(attention.waiting));
  let anyWorking = $derived(some(attention.working));
  const total = (sets: Map<string, Set<string>>) => [...sets.values()].reduce((sum, set) => sum + set.size, 0);
  let missing = $derived(waiting.length < total(attention.waiting) || working.length < total(attention.working));
  const roomHref = (room: string) => "/r/" + encodeURIComponent(room);
  // Hosts load when Home opens, then every 10 s to match the engine's beat, only while the page is visible.
  // A failed call keeps the last answer, and the next tick tries again. A tick skips while a call is out, so
  // calls never pile up on a slow engine and answers arrive in order.
  let hosts: HostSummary[] | undefined = $state();
  let hostsFailed = $state(false);
  $effect(() => {
    let timer: ReturnType<typeof setInterval> | undefined;
    let loading = false;
    const tick = () => {
      if (loading) return;
      loading = true;
      // A control plane that reports no load shows no card, never an empty one.
      void call("host.list", {}).then(list => { hosts = (list.hosts ?? []).filter(h => h.name !== "control-plane" || hostMeters(h).length); hostsFailed = false; }, () => { hostsFailed = true; })
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
  <!-- Waiting and Working are one list card each, side by side, with their empty state inside. -->
  <div class="cards top">
    <section class="card"><h2>Waiting for you{#if waiting.length}<span>{waiting.length}</span>{/if}</h2>
      {#each waiting as { id, room, value, who, root } (id)}<a class="item" href={roomHref(room.id)}
        onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
        <Avatar kind={value.by.kind} name={who} {root} size={28} />
        <span class="lines"><span class="title">{value.title}</span><span class="line">{who} · {room.name}</span></span></a>{/each}
      {#if !anyWaiting}<p class="none">Nothing is waiting for you.</p>{/if}</section>
    <section class="card"><h2>Working{#if working.length}<span>{working.length}</span>{/if}</h2>
      {#each working as { id, room, value } (id)}<a class="item" href={`${roomHref(room.id)}/s/${encodeURIComponent(id)}`} onclick={navigate}>
        <SessionRow name={value.name} root={value.root} status={["working", attention.activity.get(id), room.name].filter(Boolean).join(" · ")} /></a>{/each}
      {#if !anyWorking}<p class="none">No session is working.</p>{/if}</section>
  </div>
  {#if failed && missing}<p class="failed">Some items could not load. <button type="button" class="retry" onclick={() => { failed = false; attempt++; }}>Try again</button></p>{/if}
  {#if hosts?.length || hostsFailed}<h2>Machines{#if hosts?.length}<span>{hosts.length}</span>{/if}</h2>{/if}
  {#if !hosts && hostsFailed}<p class="failed">Machines could not load.</p>{/if}
  {#if hosts?.length}<div class="cards">{#each hosts as host (host.name)}<HostCard {host} />{/each}</div>{/if}
  {#if accounts?.length || accountsFailed}<h2>Usage{#if accounts?.length}<span>{accounts.length}</span>{/if}</h2>{/if}
  {#if accountsFailed}<p class="failed">Model usage could not load.</p>{/if}
  {#if accounts?.length}<div class="cards">{#each accounts as account, i (i)}<UsageCard {account} now={loadedAt} />{/each}</div>{/if}
</div>

<style>
  /* Home is a dashboard, so its card grids use the page width up to 1200 px. */
  .home { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; width: min(1200px, 100%); margin: 0 auto; padding: 16px 16px 32px; }
  .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 12px; margin: 0 8px; }
  .top { grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); margin-top: 8px; }
  h2 { display: flex; align-items: baseline; gap: 8px; margin: 24px 8px 12px; font-size: 18px; font-weight: 600; line-height: 1.25; }
  .card h2 { margin: 0 0 8px; font-size: 16px; }
  h2 span { color: var(--muted); font-weight: 400; font-variant-numeric: tabular-nums; }
  .item { display: flex; align-items: center; gap: 10px; min-height: 44px; margin: 0 -8px; padding: 6px 8px; border-radius: 8px; color: var(--text); text-decoration: none; }
  .item:hover { background: var(--hover); }
  .none { margin: 12px 0 4px; color: var(--muted); font-size: 13px; }
  .failed { margin: 12px 8px; color: var(--muted); font-size: 13px; }
  /* A request row has SessionRow's shape: a main line, then one muted line that ends with the room. */
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title, .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-weight: 600; font-size: 14px; }
  .line { color: var(--muted); font-size: 12px; }
</style>
