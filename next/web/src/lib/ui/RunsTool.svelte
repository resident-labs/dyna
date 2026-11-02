<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import type { WorkflowInfo } from "../contract.gen.ts";
  import { formatTime } from "../feed.ts";
  import { runCounts, runWords } from "../workflow.ts";
  // The room header's Runs tool: the room's finished workflow runs, as Jobs lists its jobs. A click opens a popover
  // under it, or a sheet on a phone, with the newest run first; each row links to the run's page.
  let { runs, room, session, phone, navigate }: {
    runs: WorkflowInfo[]; room: string; session?: string; phone: boolean;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  } = $props();

  let open = $state(false);
  let button: HTMLButtonElement | undefined = $state();
  let opener: HTMLElement | undefined;
  let list: HTMLElement | undefined = $state();
  let root: HTMLElement | undefined = $state();
  let label = $derived(`Past runs, ${runs.length}`);

  // On a phone the room menu opens it too.
  export async function show() {
    opener = document.activeElement instanceof HTMLElement ? document.activeElement : undefined;
    open = true;
    await tick();
    (list?.querySelector<HTMLElement>("a.run") ?? list)?.focus();
  }
  async function close() {
    open = false;
    await tick();
    (opener?.isConnected ? opener : button)?.focus();
  }
  function pick(event: MouseEvent & { currentTarget: HTMLAnchorElement }) {
    navigate(event);
    if (event.defaultPrevented) open = false;
  }
  // Up and Down move between the rows, Escape closes and returns to the button.
  function keys(event: KeyboardEvent) {
    if (event.key === "Escape") { event.stopPropagation(); void close(); return; }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" || !list) return;
    event.preventDefault();
    const rows = Array.from(list.querySelectorAll<HTMLElement>("a.run"));
    const index = rows.findIndex(row => row === document.activeElement);
    rows[Math.max(0, Math.min(rows.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)))]?.focus();
  }
</script>

<svelte:document onpointerdown={event => { if (open && !phone && event.target instanceof Node && !root?.contains(event.target)) open = false; }} />
<span class="runs" bind:this={root}>
  <button type="button" class="open" class:phone bind:this={button} aria-haspopup="dialog" aria-expanded={open}
    aria-label={label} title="Past runs" onclick={() => { if (open) void close(); else void show(); }}><Icon name="tool" /></button>
  {#if open}
    {#if phone}<button type="button" class="scrim" tabindex="-1" aria-label="Close the past runs" onclick={() => void close()}></button>{/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions (the arrow keys and Escape act on the whole list) -->
    <div class="pop" class:sheet={phone} role="dialog" tabindex="-1" aria-label="Past runs" onkeydown={keys}>
      {#if phone}<span class="grab"></span>{/if}
      <div class="head"><strong>Past runs</strong><span class="counts">{runs.length}</span></div>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (the list takes the arrow keys) -->
      <div class="list" role="group" aria-label="Past workflow runs" tabindex="-1" bind:this={list}>
        {#each runs as run (run.session)}{@const word = runWords[run.state]}
          <a class="run" href={`/r/${encodeURIComponent(room)}/s/${encodeURIComponent(run.session)}`} onclick={pick}
            aria-current={session === run.session ? "page" : undefined} title={run.description}><strong>{run.name}</strong>
            <span class="line">{#if word.tone}<StatusPill tone={word.tone} label={word.word} word />{:else}{word.word}{/if}{#each runCounts(run) as part (part)}{` · ${part}`}{/each}{` · ${formatTime(run.ended ?? run.started)}`}</span></a>
        {/each}
      </div>
    </div>
  {/if}
</span>

<style>
  .runs { position: relative; display: flex; }
  .open { all: unset; display: flex; align-items: center; height: 30px; padding: 0 7px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .open:hover, .open[aria-expanded="true"] { background: var(--selected); color: var(--text); }
  .open:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .open.phone { justify-content: center; width: 44px; height: 44px; padding: 0; }
  .scrim { all: unset; position: fixed; inset: 0; z-index: 5; background: var(--scrim); }
  .head { flex: none; display: flex; align-items: center; gap: 8px; padding: 12px 14px 6px; }
  .head strong { font-size: 16px; }
  .sheet .head { padding: 6px 16px 8px; }
  .sheet .head strong { font-size: 18px; }
  .counts { color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .list { min-height: 0; overflow-y: auto; padding: 0 6px 6px; }
  .list:focus { outline: none; }
  .run { display: grid; min-height: 44px; align-content: center; padding: 5px 10px; border-radius: 6px; color: var(--muted); font-size: 12.5px; line-height: 1.3; text-decoration: none; }
  .run:hover, .run:focus-visible, .run[aria-current="page"] { background: var(--selected); }
  .run:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .run strong { overflow: hidden; color: var(--text); font-size: 13px; font-weight: 500; text-overflow: ellipsis; white-space: nowrap; }
  .line { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
</style>
