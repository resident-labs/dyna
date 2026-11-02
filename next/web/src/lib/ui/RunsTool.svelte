<script lang="ts">
  import { tick } from "svelte";
  import Icon from "./Icon.svelte";
  import StatusPill from "./StatusPill.svelte";
  import WorkingDot from "./WorkingDot.svelte";
  import type { WorkflowInfo } from "../contract.gen.ts";
  import { formatTime } from "../feed.ts";
  import { minutesLabel, runCounts, runStatus, runWords } from "../workflow.ts";
  // The room's header Runs tool: the room's workflow runs, as Jobs lists its jobs. The running ones lead, each with a green dot
  // and its status line, then the rest, the latest to end first. A click opens a popover under it, or a sheet on a phone; each
  // row links to the run's page. The button has a green dot while a run is going.
  let { running, past, room, session, phone, navigate }: {
    running: WorkflowInfo[]; past: WorkflowInfo[]; room: string; session?: string; phone: boolean;
    navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void;
  } = $props();

  let open = $state(false);
  let button: HTMLButtonElement | undefined = $state();
  let opener: HTMLElement | undefined;
  let list: HTMLElement | undefined = $state();
  let root: HTMLElement | undefined = $state();
  let counts = $derived([...running.length ? [`${running.length} running`] : [], ...past.length ? [`${past.length} past`] : []].join(" · "));
  let label = $derived(`Runs${counts ? `, ${counts}` : ""}`);
  // A running run's age counts up while the list is open.
  let now = $state(Date.now());
  $effect(() => {
    if (!open || !running.length) return;
    now = Date.now();
    const timer = setInterval(() => { now = Date.now(); }, 30_000);
    return () => clearInterval(timer);
  });

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
  const href = (run: WorkflowInfo) => `/r/${encodeURIComponent(room)}/s/${encodeURIComponent(run.session)}`;
</script>

<svelte:document onpointerdown={event => { if (open && !phone && event.target instanceof Node && !root?.contains(event.target)) open = false; }} />
<span class="runs" bind:this={root}>
  <button type="button" class="open" class:phone bind:this={button} aria-haspopup="dialog" aria-expanded={open}
    aria-label={label} title={running.length ? `Runs, ${running.length} running` : "Runs"} onclick={() => { if (open) void close(); else void show(); }}><Icon name="tool" />{#if running.length}<span class="live"></span>{/if}</button>
  {#if open}
    {#if phone}<button type="button" class="scrim" tabindex="-1" aria-label="Close the runs" onclick={() => void close()}></button>{/if}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions (the arrow keys and Escape act on the whole list) -->
    <div class="pop" class:sheet={phone} role="dialog" tabindex="-1" aria-label="Runs" onkeydown={keys}>
      {#if phone}<span class="grab"></span>{/if}
      <div class="head"><strong>Runs</strong><span class="counts">{counts}</span></div>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (the list takes the arrow keys) -->
      <div class="list" role="group" aria-label="Workflow runs" tabindex="-1" bind:this={list}>
        {#if running.length}
          <p class="section">Running</p>
          {#each running as run (run.session)}{@const line = runStatus(run)}
            <a class="run" href={href(run)} onclick={pick} aria-current={session === run.session ? "page" : undefined} title={run.description}>
              <span class="dot"><WorkingDot /></span><strong>{run.name}</strong><span class="when">{minutesLabel(Math.max(0, now - Date.parse(run.started)))}</span>
              <span class="line" class:said={line.script}>{line.text}</span></a>
          {/each}
        {/if}
        {#if past.length}
          {#if running.length}<p class="section">Past</p>{/if}
          {#each past as run (run.session)}{@const word = runWords[run.state]}
            <a class="run" href={href(run)} onclick={pick} aria-current={session === run.session ? "page" : undefined} title={run.description}>
              <span class="dot"></span><strong>{run.name}</strong><span class="when">{formatTime(run.ended ?? run.started)}</span>
              <span class="line">{#if word.tone}<StatusPill tone={word.tone} label={word.word} word />{:else}{word.word}{/if}{#each runCounts(run) as part (part)}{` · ${part}`}{/each}{#if run.status || run.description}{` · ${run.status || run.description}`}{/if}</span></a>
          {/each}
        {/if}
      </div>
    </div>
  {/if}
</span>

<style>
  .runs { position: relative; display: flex; }
  .open { all: unset; position: relative; display: flex; align-items: center; height: 30px; padding: 0 7px; border-radius: 6px; color: var(--muted); cursor: pointer; }
  .open:hover, .open[aria-expanded="true"] { background: var(--selected); color: var(--text); }
  .open:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .open.phone { justify-content: center; width: 44px; height: 44px; padding: 0; }
  /* A run is going: a small green dot on the button's corner, ringed so it reads on any header. */
  .live { position: absolute; top: 5px; right: 4px; width: 7px; height: 7px; border-radius: 50%; background: var(--success); box-shadow: 0 0 0 2px var(--bg); }
  .open.phone .live { top: 11px; right: 10px; }
  .scrim { all: unset; position: fixed; inset: 0; z-index: 5; background: var(--scrim); }
  .head { flex: none; display: flex; align-items: baseline; gap: 8px; padding: 12px 14px 6px; }
  .head strong { font-size: 16px; }
  .sheet .head { padding: 6px 16px 8px; }
  .sheet .head strong { font-size: 18px; }
  .counts { color: var(--muted); font-size: 13px; font-variant-numeric: tabular-nums; }
  .list { min-height: 0; overflow-y: auto; padding: 0 6px 6px; }
  .list:focus { outline: none; }
  .section { margin: 6px 10px 2px; color: var(--muted); font-size: 12.5px; font-weight: 600; }
  .section + .run { margin-top: 2px; }
  .run + .section { margin-top: 10px; padding-top: 8px; border-top: 1px solid var(--line); }
  .run { display: grid; grid-template-columns: 12px minmax(0, 1fr) auto; gap: 2px 8px; align-items: baseline; min-height: 44px; align-content: center; padding: 5px 10px;
    border-radius: 6px; color: var(--muted); font-size: 12.5px; line-height: 1.3; text-decoration: none; }
  .run:hover, .run:focus-visible, .run[aria-current="page"] { background: var(--selected); }
  .run:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .dot { display: flex; align-self: center; }
  .run strong { overflow: hidden; color: var(--text); font-size: 13px; font-weight: 500; text-overflow: ellipsis; white-space: nowrap; }
  .when { font-size: 11px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .line { grid-column: 2 / 4; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .line.said { color: var(--text); }
</style>
