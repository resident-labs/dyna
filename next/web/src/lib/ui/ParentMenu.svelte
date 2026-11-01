<script lang="ts">
  import { call } from "../client.ts";
  import MenuList from "./MenuList.svelte";
  // The Move control beside a session's Parent name. It lists the candidates the caller passes: the room's sessions,
  // minus the session and its descendants. The row shows the store's parent, which session.moved updates, so the name
  // changes only once the engine has moved the session. A failed move shows a muted line, as Stop does.
  let { session, parent, candidates }: { session: string; parent: string; candidates: { session: string; name: string }[] } = $props();
  let open = $state(false), busy = $state(false), failure = $state("");
  let root: HTMLDivElement;
  let button: HTMLButtonElement | undefined = $state();
  async function show(next: boolean, refocus = false) {
    if (busy) return;
    open = next;
    if (!open && refocus) button?.focus();
  }
  async function choose(next: string) {
    await show(false, true);
    if (next === parent) return;
    busy = true;
    failure = "";
    await call("session.move", { session, newParent: next }).catch(() => { failure = "The session did not move. Try again."; });
    busy = false;
  }
</script>

<svelte:window onpointerdown={event => { if (open && event.target instanceof Node && !root.contains(event.target)) void show(false); }} />
<div class="menu" bind:this={root}>
  <button type="button" class="move" bind:this={button} aria-haspopup="menu" aria-expanded={open} disabled={busy}
    onclick={() => void show(!open)}>Move</button>
  {#if open}
    <MenuList label="Parents" items={candidates} keyOf={entry => entry.session} checked={parent} anchor={root} onchoose={name => void choose(name)} onclose={refocus => void show(false, refocus)}>
      {#snippet row(entry)}<span>{entry.name}</span>{/snippet}
    </MenuList>
  {/if}
</div>
{#if failure}<span class="failed">{failure}</span>{/if}

<style>
  .menu { display: inline-block; position: relative; margin-left: 8px; }
  .move { padding: 0; border: 0; border-radius: 4px; background: none; color: var(--muted); font: inherit; font-size: 12.5px; cursor: pointer; }
  .move:hover:not(:disabled), .move[aria-expanded="true"] { color: var(--text); text-decoration: underline; }
  .move:disabled { opacity: 0.6; cursor: default; }
  .move:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  @media (hover: none) { .move { min-height: 40px; } }
  .failed { display: block; color: var(--muted); font-size: 12.5px; }
</style>
