<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  // The room's less frequent actions. On a phone they sit behind the header's button; elsewhere a right click on the
  // room's rail icon opens them at the pointer, and focus returns to that icon.
  let { phone, paused, operate, admin, onpause, oninstructions, onterminal, oncomputer, onrequests, onjobs, jobFailed = false, waiting = 0 }: { phone: boolean; onrequests?: () => void; onjobs?: () => void; jobFailed?: boolean; waiting?: number; paused: boolean; operate: boolean; admin: boolean; onpause: () => void; oninstructions: () => void; onterminal?: () => void; oncomputer?: () => void } = $props();
  let open = $state(false);
  let root: HTMLDivElement;
  let button: HTMLButtonElement | undefined = $state();
  let at: { x: number; y: number; trigger: HTMLElement } | undefined = $state();
  let list: HTMLDivElement | undefined = $state();
  const items = () => Array.from(list?.querySelectorAll<HTMLButtonElement>("[role=menuitem]") ?? []);
  async function show(next: boolean, refocus = false) {
    open = next;
    if (next) {
      await tick();
      if (at && list) at = { ...at, x: Math.min(at.x, innerWidth - list.offsetWidth - 8), y: Math.min(at.y, innerHeight - list.offsetHeight - 8) };
      items()[0]?.focus();
    } else {
      const trigger = at?.trigger;
      at = undefined;
      if (refocus) (trigger ?? button)?.focus();
    }
  }
  export function openAt(point: { x: number; y: number; trigger: HTMLElement }) { at = point; void show(true); }
  // The list leaves the header at a pointer, so a container in the header cannot place or clip it.
  function atPointer(node: HTMLElement, moved: boolean) { if (moved) document.body.append(node); return { destroy() { if (moved) node.remove(); } }; }
  // A page the menu opened returns focus here when it closes.
  export function focus(options?: FocusOptions) { button?.focus(options); }
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

<svelte:window onpointerdown={event => { if (open && event.target instanceof Node && !root.contains(event.target) && !list?.contains(event.target)) void show(false); }} />
<div class="menu" bind:this={root}>
  {#if phone}<button type="button" class="open" bind:this={button} aria-haspopup="menu" aria-expanded={open} aria-controls="room-menu"
    aria-label={menuLabel} title={menuLabel}
    onclick={() => void show(!open)} onkeydown={event => { if (event.key === "ArrowDown") { event.preventDefault(); void show(true); } }}>
    <Icon name="more" />{#if waiting}<StatusPill tone="warning" label="Requests waiting" count={waiting} />{/if}{#if jobFailed}<StatusPill tone="failed" label="A job failed" dot />{/if}</button>{/if}
  {#if open}
    <div class="list" class:pointer={at} style:left={at && `${at.x}px`} style:top={at && `${at.y}px`} role="menu" id="room-menu" aria-label="Room menu" tabindex="-1" bind:this={list} onkeydown={move} use:atPointer={!!at}>
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
  .menu:empty { display: none; }
  .open { display: flex; align-items: center; gap: 4px; padding: 6px; border: 0; border-radius: 6px; background: none; color: var(--muted); cursor: pointer; }
  @media (max-width: 640px) { .open { justify-content: center; min-width: 44px; min-height: 44px; } }
  .open:hover, .open[aria-expanded="true"] { background: var(--hover); color: var(--text); }
  .list { position: absolute; right: 0; top: calc(100% + 4px); z-index: 2; min-width: 180px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px var(--shadow-md); }
  .list.pointer { position: fixed; right: auto; z-index: 30; }
  [role="menuitem"] { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 6px 8px; border: 0; border-radius: 6px; background: none; color: var(--text); font: inherit; font-size: 14px; text-align: left; cursor: pointer; }
  /* Items have no border or ring of their own; a fill marks the hovered or focused one. */
  [role="menuitem"]:hover, [role="menuitem"]:focus { background: var(--selected); outline: none; }
</style>
