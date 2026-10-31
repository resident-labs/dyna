<script lang="ts">
  import { SvelteMap } from "svelte/reactivity";
  import { call } from "../client.ts";
  import type { Attention } from "../attention.svelte.ts";
  import SessionRow from "./SessionRow.svelte";
  type Navigate = (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  // onrequests opens the Requests panel once the link has moved to that room.
  let { attention, navigate, onrequests }: { attention: Attention; navigate: Navigate; onrequests: () => void } = $props();
  // Request titles and session names load per room while Home is open, and again only when a room
  // gains an ID that no answer named yet. A failed load leaves those rows out.
  const titles = new SvelteMap<string, string>();
  const sessions = new SvelteMap<string, { name: string; root: boolean }>();
  const asked = new Set<string>();
  const unseen = (ids: Iterable<string>, known: SvelteMap<string, unknown>) => {
    const fresh = [...ids].filter(id => !known.has(id) && !asked.has(id));
    for (const id of fresh) asked.add(id);
    return fresh.length > 0;
  };
  $effect(() => {
    for (const room of attention.rooms.keys()) {
      if (unseen(attention.waiting.get(room) ?? [], titles)) void call("op.list", { room }).then(list => {
        for (const op of list.operations ?? []) titles.set(op.id, op.card.title);
      }, () => {});
      if (unseen(attention.working.get(room) ?? [], sessions)) void call("session.list", { room }).then(list => {
        for (const item of list.sessions ?? []) sessions.set(item.id, { name: item.name, root: !item.parent });
      }, () => {});
    }
  });
  const rows = <T,>(sets: Map<string, Set<string>>, known: SvelteMap<string, T>) => [...attention.rooms.values()].flatMap(room =>
    [...sets.get(room.id) ?? []].flatMap(id => { const value = known.get(id); return value === undefined ? [] : [{ id, room, value }]; }));
  let requests = $derived(rows(attention.waiting, titles));
  let working = $derived(rows(attention.working, sessions));
  // Counts come from the store, not the loaded rows: as in v1's Home, a failed load is never "all clear".
  const count = (sets: Map<string, Set<string>>) => [...sets.values()].reduce((sum, set) => sum + set.size, 0);
  let waitingCount = $derived(count(attention.waiting));
  let workingCount = $derived(count(attention.working));
  const roomHref = (room: string) => "/r/" + encodeURIComponent(room);
</script>

<div class="home">
  {#if waitingCount}<h2 class="label">Waiting for you<span>{waitingCount}</span></h2>
    {#each requests as { id, room, value } (id)}<a class="item" href={roomHref(room.id)}
      onclick={event => { navigate(event); if (event.defaultPrevented) onrequests(); }}>
      <span class="title">{value}</span><span class="room">{room.name}</span></a>{/each}{/if}
  {#if workingCount}<h2 class="label">Working<span>{workingCount}</span></h2>
    {#each working as { id, room, value } (id)}<a class="item" href={`${roomHref(room.id)}/s/${encodeURIComponent(id)}`} onclick={navigate}>
      <SessionRow name={value.name} root={value.root} status={room.name} /></a>{/each}{/if}
  {#if !waitingCount && !workingCount}<p class="empty">Nothing waits for you, and no session is working.</p>{/if}
</div>

<style>
  .home { flex: 1; min-height: 0; overflow-y: auto; display: flex; flex-direction: column; width: min(640px, 100%); margin: 0 auto; padding: 16px 8px; }
  h2 { display: flex; justify-content: space-between; margin: 16px 8px 4px; }
  h2 span { font-weight: 400; font-variant-numeric: tabular-nums; }
  .item { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 6px 8px; border-radius: 8px; color: var(--text); text-decoration: none; }
  .item:hover { background: var(--hover); }
  .title, .room { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { flex: 1; min-width: 0; font-size: 14px; }
  .room { color: var(--muted); font-size: 12px; }
</style>
