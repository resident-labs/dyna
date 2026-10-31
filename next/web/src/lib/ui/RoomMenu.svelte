<script lang="ts" module>
  // Each room view behind the menu is one entry; a new view adds one line here.
  const entries = [{ id: "requests", label: "Requests" }] as const;
  export type Section = (typeof entries)[number]["id"];
</script>

<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  let { waiting, onchoose }: { waiting: number; onchoose: (section: Section) => void } = $props();
  // The count each entry shows at its right, only while it is above zero.
  let counts: Record<Section, number> = $derived({ requests: waiting });
  let open = $state(false);
  let root: HTMLDivElement;
  let button: HTMLButtonElement;
  let list: HTMLDivElement | undefined = $state();
  export function focus() { button.focus(); }
  const items = () => Array.from(list?.querySelectorAll<HTMLButtonElement>("[role=menuitem]") ?? []);
  async function show(next: boolean, refocus = false) {
    open = next;
    if (next) { await tick(); items()[0]?.focus(); } else if (refocus) button.focus();
  }
  // Up and Down move between entries, Home and End jump, Escape closes and Tab leaves.
  function move(event: KeyboardEvent) {
    const all = items();
    const index = all.findIndex(item => item === document.activeElement);
    const to = { ArrowDown: index + 1, ArrowUp: index - 1, Home: 0, End: all.length - 1 }[event.key];
    if (to !== undefined) { event.preventDefault(); all[(to + all.length) % all.length]?.focus(); }
    if (event.key === "Escape") { event.stopPropagation(); void show(false, true); }
    if (event.key === "Tab") void show(false);
  }
</script>

<svelte:window onpointerdown={event => { if (open && event.target instanceof Node && !root.contains(event.target)) void show(false); }} />
<div class="menu" bind:this={root}>
  <button type="button" class="open" bind:this={button} aria-haspopup="menu" aria-expanded={open} aria-controls="room-menu"
    aria-label={waiting ? `Room menu, ${waiting} ${waiting === 1 ? "request waits" : "requests wait"} for you` : "Room menu"} title="Room menu"
    onclick={() => void show(!open)} onkeydown={event => { if (event.key === "ArrowDown") { event.preventDefault(); void show(true); } }}>
    <Icon name="more" />{#if waiting}<StatusPill tone="warning">{waiting}</StatusPill>{/if}</button>
  {#if open}
    <div class="list" role="menu" id="room-menu" tabindex="-1" bind:this={list} onkeydown={move}>
      {#each entries as entry (entry.id)}
        <button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false); onchoose(entry.id); }}>{entry.label}
          {#if counts[entry.id]}<StatusPill tone="warning">{counts[entry.id]}</StatusPill>{/if}</button>
      {/each}
    </div>
  {/if}
</div>

<style>
  /* The menu takes the free header space, so the sessions toggle after it sits at the edge. */
  .menu { position: relative; margin-left: auto; }
  :global(.menu ~ .panel-toggle) { margin-left: 0; }
  .open { display: flex; align-items: center; gap: 6px; padding: 6px; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  .open:hover, .open[aria-expanded="true"] { background: var(--hover); color: var(--text); }
  .list { position: absolute; right: 0; top: calc(100% + 4px); z-index: 2; min-width: 180px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px rgb(0 0 0 / 18%); }
  [role="menuitem"] { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 14px; text-align: left; cursor: pointer; }
  /* Items have no border or ring of their own; a fill marks the hovered or focused one. */
  [role="menuitem"]:hover, [role="menuitem"]:focus { background: var(--selected); outline: none; }
</style>
