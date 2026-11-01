<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  // The room's less frequent actions. Requests has its own header button.
  let { paused, operate, admin, onpause, oninstructions, onnewsession, onterminal, oncomputer, onrequests, onjobs, jobFailed = false, waiting = 0 }: { onrequests?: () => void; onjobs?: () => void; jobFailed?: boolean; waiting?: number; paused: boolean; operate: boolean; admin: boolean; onpause: () => void; oninstructions: () => void; onnewsession: () => void; onterminal?: () => void; oncomputer?: () => void } = $props();
  let open = $state(false);
  let root: HTMLDivElement;
  let button: HTMLButtonElement;
  let list: HTMLDivElement | undefined = $state();
  const items = () => Array.from(list?.querySelectorAll<HTMLButtonElement>("[role=menuitem]") ?? []);
  async function show(next: boolean, refocus = false) {
    open = next;
    if (next) { await tick(); items()[0]?.focus(); } else if (refocus) button.focus();
  }
  // A page the menu opened returns focus here when it closes.
  export function focus(options?: FocusOptions) { button.focus(options); }
  let waitingLabel = $derived(`${waiting} ${waiting === 1 ? "request" : "requests"} waiting`);
  let menuLabel = $derived(["Room menu", waiting ? waitingLabel : "", jobFailed ? "a job failed" : ""].filter(Boolean).join(", "));
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
    aria-label={menuLabel} title={menuLabel}
    onclick={() => void show(!open)} onkeydown={event => { if (event.key === "ArrowDown") { event.preventDefault(); void show(true); } }}>
    <Icon name="more" />{#if waiting}<StatusPill tone="warning" label="Requests waiting" count={waiting} />{/if}{#if jobFailed}<StatusPill tone="failed" label="A job failed" dot />{/if}</button>
  {#if open}
    <div class="list" role="menu" id="room-menu" tabindex="-1" bind:this={list} onkeydown={move}>
      {#if operate}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false); onnewsession(); }}>New session</button>{/if}
      {#if onrequests}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false, true); onrequests(); }}>Requests{#if waiting}<StatusPill tone="warning" label="Requests waiting" count={waiting} />{/if}</button>{/if}
      {#if onjobs}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false, true); onjobs(); }}>Jobs{#if jobFailed}<StatusPill tone="failed" label="A job failed" dot />{/if}</button>{/if}
      {#if onterminal}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false, true); onterminal(); }}>Terminal</button>{/if}
      {#if oncomputer}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false, true); oncomputer(); }}>Computer</button>{/if}
      {#if admin}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false, true); oninstructions(); }}>Instructions</button>{/if}
      {#if operate}<button type="button" role="menuitem" tabindex="-1" onclick={() => { void show(false, true); onpause(); }}>{paused ? "Resume room" : "Pause room"}</button>{/if}
    </div>
  {/if}
</div>

<style>
  .menu { position: relative; }
  .open { display: flex; align-items: center; gap: 4px; padding: 6px; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  @media (max-width: 640px) { .open { justify-content: center; min-width: 44px; min-height: 44px; } }
  .open:hover, .open[aria-expanded="true"] { background: var(--hover); color: var(--text); }
  .list { position: absolute; right: 0; top: calc(100% + 4px); z-index: 2; min-width: 180px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px var(--shadow-md); }
  [role="menuitem"] { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 14px; text-align: left; cursor: pointer; }
  /* Items have no border or ring of their own; a fill marks the hovered or focused one. */
  [role="menuitem"]:hover, [role="menuitem"]:focus { background: var(--selected); outline: none; }
</style>
