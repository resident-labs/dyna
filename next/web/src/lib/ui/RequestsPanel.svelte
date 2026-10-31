<script lang="ts">
  import { onMount } from "svelte";
  import PageCrumb from "./PageCrumb.svelte";
  import RequestCard from "./RequestCard.svelte";
  import type { Request, RoomStore } from "../room.svelte.ts";
  let { store, room, onback, element = $bindable() }: { store: RoomStore; room: string; onback: (event: MouseEvent) => void; element?: HTMLElement } = $props();
  // Relative times refresh each half minute while the page is open; this reads nothing from the engine.
  let now = $state(Date.now());
  // Waiting requests in the order they expire: first the owner's own, which Home and the room header count, then the ones
  // a session above the asker may also decide. Then the ones decided today, newest first, folded after 10.
  const byExpiry = (a: Request, b: Request) => Date.parse(a.expires) - Date.parse(b.expires);
  let requests = $derived([...store.requests.values()]);
  let waiting = $derived(requests.filter(request => request.state === "pending" && request.class === "owner").sort(byExpiry));
  let shared = $derived(requests.filter(request => request.state === "pending" && request.class !== "owner").sort(byExpiry));
  let pending = $derived([...waiting, ...shared]);
  let today = $derived(new Date(now).toDateString());
  let decided = $derived(requests.filter(request => request.decision && new Date(request.decision.at).toDateString() === today)
    .sort((a, b) => (b.decision?.at ?? "").localeCompare(a.decision?.at ?? "")));
  let all = $state(false);
  // One waiting request is open, the first until the owner opens another; the keys act on the current one.
  // Both fall back to the first when theirs is decided.
  let chosen = $state("");
  let pointed = $state("");
  let open = $derived(pending.some(request => request.id === chosen) ? chosen : pending[0]?.id);
  let current = $derived(pending.some(request => request.id === pointed) ? pointed : open);
  let cards: Record<string, ReturnType<typeof RequestCard>> = $state({});
  // J and K move, A approves, D denies and Enter opens, as Linear's inbox; typing in the note field is left alone.
  // A held key never decides: the current request moves on, so a repeat would decide one the owner has not read.
  function keys(event: KeyboardEvent) {
    if (event.target instanceof HTMLInputElement || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || !current) return;
    const at = pending.findIndex(request => request.id === current);
    const key = event.key.toLowerCase();
    if (key === "j" || key === "k") pointed = pending[Math.min(pending.length - 1, Math.max(0, at + (key === "j" ? 1 : -1)))].id;
    else if (key === "a" || key === "d") { if (!event.repeat) void cards[current]?.decide(key === "a"); }
    else if (key === "enter" && !(event.target instanceof HTMLButtonElement)) chosen = current;
    else return;
    event.preventDefault();
  }
  // A decided row loses its buttons, and focus with them; it returns to the page, so the keys keep working.
  $effect(() => {
    void pending.length;
    if (document.activeElement === document.body) element?.focus();
  });
  // The keys can move past the fold, and the current row stays in view.
  $effect(() => {
    void current;
    element?.querySelector(".current")?.scrollIntoView({ block: "nearest" });
  });
  onMount(() => {
    // Focus moves to the page, so the keys work at once.
    element?.focus();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });
</script>

{#snippet card(request: Request)}
  <RequestCard bind:this={cards[request.id]} {request} {store} {now} folded={request.id !== open} current={request.id === current}
    onopen={() => { chosen = request.id; pointed = request.id; }} />
{/snippet}

<!-- The page takes focus and its keys are shortcuts for the buttons on each row, which stay the accessible way to decide. -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section class="panel" aria-label="Requests" tabindex="-1" bind:this={element} onkeydown={keys}>
  <div class="top"><PageCrumb {room} page="Requests" {onback} />
    {#if pending.length}<span class="hints" aria-hidden="true"><span><kbd>A</kbd> approve</span><span><kbd>D</kbd> deny</span><span><kbd>J</kbd><kbd>K</kbd> move</span></span>{/if}</div>
  <div class="list"><div class="column">
    <h2>Waiting for you{#if waiting.length}<span class="count">{waiting.length}</span>{/if}</h2>
    {#each waiting as request (request.id)}{@render card(request)}
    {:else}{#if store.ready}<p class="none">Nothing waits for you.</p>{/if}{/each}
    {#if shared.length}
      <h2>Waiting for a session<span class="count">{shared.length}</span></h2>
      {#each shared as request (request.id)}{@render card(request)}{/each}
    {/if}
    {#if decided.length}
      <h2>Decided today<span class="count">{decided.length}</span></h2>
      {#each all ? decided : decided.slice(0, 10) as request (request.id)}<RequestCard {request} {store} {now} />{/each}
      {#if decided.length > 10 && !all}<button type="button" class="more" onclick={() => { all = true; }}>Show {decided.length - 10} more</button>{/if}
    {/if}
  </div></div>
</section>

<style>
  .panel { flex: 1; min-height: 0; display: flex; flex-direction: column; }
  /* The page holds focus for its keys; the current row's background shows where they act, so the page needs no ring. */
  .panel:focus-visible { outline: none; }
  .top { display: flex; align-items: center; gap: 8px; padding: 10px 16px; border-bottom: 1px solid var(--line); }
  /* Key hints only where a keyboard is likely: wide screens that can hover. */
  .hints { margin-left: auto; display: none; align-items: center; gap: 12px; color: var(--muted); font-size: 12.5px; }
  .hints span { display: flex; align-items: center; gap: 4px; }
  @media (hover: hover) and (min-width: 641px) { .hints { display: flex; } }
  kbd { min-width: 18px; padding: 0 4px; border: 1px solid var(--line); border-radius: 4px; font: 11px/16px var(--mono); text-align: center; }
  .list { flex: 1; min-height: 0; overflow-y: auto; }
  /* One reading column, as the room's posts; the open card spans it. */
  .column { max-width: 760px; margin: 0 auto; padding: 8px 8px 32px; }
  .column > :global(.card) { max-width: none; margin: 4px 0 8px; }
  h2 { display: flex; align-items: baseline; gap: 8px; margin: 0; padding: 20px 12px 6px; font-size: 18px; font-weight: 600; }
  .count { color: var(--muted); font-size: 13px; font-weight: 400; font-variant-numeric: tabular-nums; }
  .none { margin: 0; padding: 8px 12px; color: var(--muted); font-size: 14px; }
  .more { margin: 4px 12px; padding: 4px 8px; border: 0; border-radius: 6px; background: none; color: var(--muted); font: inherit; font-size: 13px; cursor: pointer; }
  .more:hover { background: var(--hover); color: var(--text); }
</style>
