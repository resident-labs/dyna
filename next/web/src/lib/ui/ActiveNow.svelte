<script lang="ts">
  import type { RoomLine } from "../active.ts";
  import { count } from "../feed.ts";
  import Avatar from "./Avatar.svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import VirtualList from "./VirtualList.svelte";
  // One row per active room, never one per session. Quiet rooms fold into the last row, and open under it.
  let { active, quiet, initials, navigate }: {
    active: RoomLine[]; quiet: RoomLine[]; initials: Map<string, string>;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  } = $props();
  type Row = RoomLine | "quiet";
  let open = $state(false);
  let rows: Row[] = $derived([...active, ...quiet.length ? ["quiet" as const] : [], ...open ? quiet : []]);
</script>

<VirtualList items={rows} key={row => row === "quiet" ? row : row.room.id} label="Active rooms" role="group" align="top" estimate={46}>
  {#snippet row(item: Row)}
    {#if item === "quiet"}<button type="button" class="row quiet" aria-expanded={open} onclick={() => { open = !open; }}><Icon name="chevron" />{count(quiet.length, "quiet room")}</button>
    {:else}{@const { room, failed, words, command } = item}<a class="row" href={"/r/" + encodeURIComponent(room.id)} onclick={navigate}>
      <Avatar kind="room" name={room.name} label={initials.get(room.name)} size={28} />
      <span class="lines"><span class="title">{room.name}</span>
        <span class="line">{#if failed}<StatusPill tone="failed" label="{failed} failed" />{words && " · "}{/if}{words}{#if command}{" "}<code>{command}</code>{/if}</span></span></a>{/if}
  {/snippet}
</VirtualList>

<style>
  /* The room opens on a click, so a row takes a hover wash. */
  .row { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 44px; padding: 6px 8px; border-radius: 6px; color: var(--text); text-decoration: none; }
  @media (hover: hover) { .row:hover { background: var(--hover); } }
  .lines { flex: 1; min-width: 0; display: grid; line-height: 1.3; }
  .title, .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title { font-size: 14px; font-weight: 600; }
  .line { color: var(--muted); font-size: 12px; }
  .line :global(.status) { font-size: 12px; }
  code { font: 12px var(--mono); }
  .quiet { min-height: 36px; border: 0; background: none; color: var(--muted); font: inherit; font-size: 12px; text-align: left; cursor: pointer; }
  .quiet :global(svg) { flex: none; transform: rotate(-90deg); }
  .quiet[aria-expanded="true"] :global(svg) { transform: none; }
</style>
