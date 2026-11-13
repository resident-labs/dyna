<script lang="ts">
  import { roomChoices } from "../../accounts.ts";
  import type { RoomSummary } from "../../contract.gen.ts";
  import SearchBox from "./SearchBox.svelte";
  // The rooms an item serves, in a Settings detail (spec 5.13), as an account's Serves and a host's Rooms it serves:
  // Every room, or Only these rooms (an account's Preferred by: No room, or These rooms), picked from a search of the
  // rooms in the browser, the chosen first, at most 60 drawn and a count of the rest. The last chosen room cannot be unticked, so a limit is never empty. Each change saves
  // at once through set, which says whether the engine took it, and a refusal puts the control back. The controls stay
  // enabled while one saves, so the focus keeps its place, and a click meanwhile does nothing.
  let { label, rooms, chosen, busy, set, every = "Every room", only = "Only these rooms", hint = "" }: {
    label: string; rooms: readonly RoomSummary[]; chosen: readonly string[]; busy: boolean; set: (rooms: string[]) => Promise<boolean>;
    every?: string; only?: string; hint?: string;
  } = $props();
  const id = $props.id();
  let query = $state("");
  let limited = $derived(chosen.length > 0);
  let picks = $derived(roomChoices(rooms, chosen, query, 60));
  const hold = (event: MouseEvent) => { if (busy) event.preventDefault(); };
  function serve(group: Element | null, next: string[]) {
    void set(next).then(done => { if (!done) for (const radio of group?.querySelectorAll("input") ?? []) radio.checked = (radio.value === "only") === limited; });
  }
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString("en")} ${n === 1 ? one : many}`;
</script>

<div class="section"><strong class="head" id={`${id}-head`}>{label}</strong>
  <div class="radios" role="radiogroup" aria-labelledby={`${id}-head`}>
    <label class="opt"><input type="radio" name={`${id}-serves`} value="every" checked={!limited} onclick={hold}
      onchange={event => serve(event.currentTarget.closest("[role=radiogroup]"), [])} />{every}</label>
    <label class="opt"><input type="radio" name={`${id}-serves`} value="only" checked={limited} disabled={!rooms.length} onclick={hold}
      onchange={event => serve(event.currentTarget.closest("[role=radiogroup]"), [rooms[0].id])} />{only}</label></div>
  {#if hint}<p class="small muted">{hint}</p>{/if}
  {#if limited}<div class="picker">
    <SearchBox bind:value={query} label="Search rooms" placeholder={`Search ${plural(rooms.length, "room", "rooms")}`} />
    <div class="rooms"><p class="count small muted">{chosen.length} of {plural(rooms.length, "room", "rooms")} chosen</p>
      {#each picks.items as room (room.id)}{@const on = chosen.includes(room.id)}
        <label class="opt"><input type="checkbox" checked={on} disabled={on && chosen.length === 1} onclick={hold}
          onchange={event => { const box = event.currentTarget; void set(box.checked ? [...chosen, room.id] : chosen.filter(each => each !== room.id)).then(done => { if (!done) box.checked = !box.checked; }); }} /><span class="rname">{room.name}</span></label>{/each}
      {#if picks.more}<p class="count small muted">{picks.more.toLocaleString("en")} more: search to narrow</p>{:else if !picks.items.length}<p class="count small muted">No room matches “{query.trim()}”.</p>{/if}</div></div>{/if}
</div>

<style>
  .head { font-size: 14px; }
  .radios { display: flex; flex-wrap: wrap; gap: 4px 18px; }
  .opt { display: flex; align-items: center; gap: 8px; min-height: 30px; font-size: 14px; cursor: pointer; }
  .opt:has(input:disabled) { cursor: default; }
  .opt input { margin: 0; accent-color: var(--focus); cursor: inherit; }
  .picker { display: grid; gap: 8px; }
  .rooms { display: grid; max-height: 260px; overflow-y: auto; padding: 4px 12px; border: 1px solid var(--line); border-radius: 12px; background: var(--raised); }
  .rooms .count { padding: 6px 0; }
  .rname { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @media (hover: none) { .opt { min-height: 40px; } }
</style>
