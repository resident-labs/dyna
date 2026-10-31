<script lang="ts">
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { call } from "../client.ts";
  import type { Attention } from "../attention.svelte.ts";
  import type { Principal, SessionListResponse } from "../contract.gen.ts";
  import Avatar from "./Avatar.svelte";
  import SessionRow from "./SessionRow.svelte";
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
</script>

<div class="home">
  {#if anyWaiting}<h2 class="label">Waiting for you{#if waiting.length}<span>{waiting.length}</span>{/if}</h2>
    {#each waiting as { id, room, value, who, root } (id)}<a class="item" href={roomHref(room.id)}
      onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
      <Avatar kind={value.by.kind} name={who} {root} size={28} />
      <span class="lines"><span class="title">{value.title}</span><span class="line">{who} · {room.name}</span></span></a>{/each}{/if}
  {#if anyWorking}<h2 class="label">Working{#if working.length}<span>{working.length}</span>{/if}</h2>
    {#each working as { id, room, value } (id)}<a class="item" href={`${roomHref(room.id)}/s/${encodeURIComponent(id)}`} onclick={navigate}>
      <SessionRow name={value.name} root={value.root} status={["working", attention.activity.get(id), room.name].filter(Boolean).join(" · ")} /></a>{/each}{/if}
  {#if failed && missing}<p class="failed">Some items could not load. <button type="button" class="retry" onclick={() => { failed = false; attempt++; }}>Try again</button></p>{/if}
  {#if !anyWaiting && !anyWorking}<p class="empty">Nothing waits for you, and no session is working.</p>{/if}
</div>

<style>
  .home { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; width: min(640px, 100%); margin: 0 auto; padding: 16px 8px; }
  h2 { display: flex; justify-content: space-between; margin: 16px 8px 4px; }
  h2 span { font-weight: 400; font-variant-numeric: tabular-nums; }
  .item { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 6px 8px; border-radius: 8px; color: var(--text); text-decoration: none; }
  .item:hover { background: var(--hover); }
  .failed { margin: 12px 8px; color: var(--muted); font-size: 13px; }
  /* A request row has SessionRow's shape: a main line, then one muted line that ends with the room. */
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title, .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-weight: 600; font-size: 14px; }
  .line { color: var(--muted); font-size: 12px; }
</style>
