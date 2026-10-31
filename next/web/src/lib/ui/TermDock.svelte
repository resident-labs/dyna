<script lang="ts">
  import { tick } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { call } from "../client.ts";
  import type { HostSummary } from "../contract.gen.ts";
  import { hostLabel } from "../hosts.ts";
  import type { TerminalTarget } from "../terminal.ts";
  import FloatWindow from "./FloatWindow.svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import JobsPane from "./JobsPane.svelte";
  import StatusPill from "./StatusPill.svelte";
  import TermPane from "./TermPane.svelte";
  // The owner's terminals, one tab per shell, docked at the bottom of the main column or floating over
  // the app. The dock is app-wide: hosts are not room objects, so tabs stay across rooms and on Home.
  // A tab lives while the page is open (no reattach in M2); × closes its socket, and the engine ends
  // the shell. The new-tab menu lists the room's sessions whose box is ready, then the hosts.
  // After the shells, a Jobs tab shows the room's background jobs (JobsPane) while jobs has a session; it counts the
  // running jobs, then the failed jobs the owner has not opened, in the failed tone (Jobs fix 4). With no shell open,
  // the dock shows only its tab bar until the owner opens Jobs.
  type JobSession = { id: string; name: string; root: boolean; running: number };
  let { sessions, jobs, failures, element = $bindable(), onempty }: {
    sessions: { id: string; name: string; root: boolean }[]; jobs: JobSession[]; failures: string[]; element?: HTMLElement; onempty: () => void;
  } = $props();
  type Tab = { key: number; target: TerminalTarget; label: string; root: boolean; exited?: { code?: number } };
  let tabs: Tab[] = $state([]);
  let active = $state(0);
  let next = 1;
  let picking = $state(false);
  let hosts: HostSummary[] = $state([]);
  let floating = $state(false);
  let rect = $state({ x: window.innerWidth - 744, y: window.innerHeight - 464, w: 720, h: 440 });
  // The docked height, kept for the page view. Maximized, the dock takes the most height fit allows; collapsed, only its tab bar shows.
  let height = $state(280);
  let maximized = $state(false);
  let collapsed = $state(false);
  let menu: HTMLElement | undefined = $state();
  let plus: HTMLButtonElement | undefined = $state();
  const panes: Record<number, TermPane | undefined> = $state({});
  const JOBS = -1;
  let only: string | undefined = $state();
  let jobsPane: JobsPane | undefined = $state();
  const opened = new SvelteSet<string>();
  let unopened = $derived(failures.filter(key => !opened.has(key)).length);
  // The open list knows its own rows, so the tab counts those and the two cannot disagree.
  let listed: number | undefined = $state();
  let running = $derived(active === JOBS && (floating || !collapsed) && listed !== undefined ? listed : jobs.reduce((sum, session) => sum + session.running, 0));
  // Jobs that leave, as on Home or when every box stops, take their tab with them.
  $effect.pre(() => { if (active === JOBS && !jobs.length) active = tabs[0]?.key ?? 0; });
  // A session row's jobs icon opens the tab on that session's jobs.
  export function showJobs(session?: string) {
    only = session;
    void show(JOBS);
  }

  // Opens the new-tab menu; the room menu's "New terminal" comes here too.
  export async function pick() {
    picking = true;
    void call("host.list", {}).then(list => { hosts = list.hosts ?? []; }, () => { hosts = []; });
    await tick();
    // With no item yet (no ready session, hosts still loading), the menu itself takes the keys.
    (menu?.querySelector<HTMLElement>("[role=menuitem]") ?? menu)?.focus();
  }
  // Showing a tab opens a collapsed dock and moves focus into its shell.
  async function show(key: number) {
    active = key;
    collapsed = false;
    await tick();
    if (key === JOBS) jobsPane?.focus(); else panes[key]?.focus();
  }
  const liveShell = (id: string) => tabs.find(tab => "box" in tab.target && tab.target.box === id && !tab.exited);
  // Whether the session has a shell open, for the DM header's pressed Terminal button.
  export const hasShell = (id: string) => liveShell(id) !== undefined;
  // The DM header's Terminal button: it shows the session's shell, or opens one. The "+" menu always opens a new one.
  export function shell(id: string, name: string, root: boolean) {
    const tab = liveShell(id);
    if (tab) void show(tab.key); else open({ box: id }, name, root);
  }
  function open(target: TerminalTarget, label: string, root = false) {
    picking = false;
    const tab = { key: next++, target, label, root };
    tabs.push(tab);
    void show(tab.key);
  }
  // A new shell for an exited tab reopens its target under a new key, so its pane starts over.
  function renew(tab: Tab) {
    const index = tabs.findIndex(item => item.key === tab.key);
    tabs[index] = { key: next++, target: tab.target, label: tab.label, root: tab.root };
    void show(tabs[index].key);
  }
  // Focus moves to the tab that takes the closed one's place, or out of the dock when none is left. A collapsed dock
  // stays collapsed, so its tab takes the focus.
  async function close(key: number) {
    const index = tabs.findIndex(tab => tab.key === key);
    tabs.splice(index, 1);
    if (active === key) active = tabs[Math.min(index, tabs.length - 1)]?.key ?? 0;
    if (!tabs.length) floating = false;
    await tick();
    if (!floating && collapsed && active) element?.querySelector<HTMLElement>("[role=tab][aria-selected=true]")?.focus();
    else if (active === JOBS || tabs.length) await show(active); else if (picking) plus?.focus(); else onempty();
  }
  // Up and Down move in the menu, Escape closes it and returns to "+".
  function walk(event: KeyboardEvent) {
    const items = Array.from(menu?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? []);
    const at = items.findIndex(item => item === document.activeElement);
    const to = { ArrowDown: at + 1, ArrowUp: at - 1 }[event.key];
    if (to !== undefined) { event.preventDefault(); items[(to + items.length) % items.length]?.focus(); }
    if (event.key === "Escape") { event.stopPropagation(); picking = false; plus?.focus(); }
  }
  // Dragging the top edge sets the docked height, between 120 px and the main column minus 200 px.
  const fit = (value: number) => Math.max(120, Math.min(value, (element?.parentElement?.clientHeight ?? 600) - 200));
  function drag(event: PointerEvent) {
    const start = height, y = event.clientY;
    event.preventDefault();
    maximized = false;
    const move = (next: PointerEvent) => { height = fit(start + y - next.clientY); };
    const end = () => { window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", end); };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end);
  }
  // fit reads the column's height, which only a window resize changes, so the maximized height follows the window.
  let viewport = $state(window.innerHeight);
  let tallest = $derived(viewport && fit(Infinity));
  // Where the active shell runs. The engine gives no shell folder, so a box shows its session's name.
  let shown = $derived(tabs.find(tab => tab.key === active));
  let where = $derived(shown && ("box" in shown.target ? `${shown.label} box` : shown.label));
</script>

<svelte:window onresize={() => { viewport = window.innerHeight; }} />

{#if tabs.length || picking || jobs.length}
<FloatWindow label="Terminals" kind="terminals" docked={!floating} bind:rect bind:element>
  {#snippet header()}
    <div class="tabs" role="tablist" aria-label="Terminals">
      {#each tabs as tab (tab.key)}<span class="tab" class:active={tab.key === active}>
        <button type="button" role="tab" aria-selected={tab.key === active} onclick={() => void show(tab.key)}>{#if "box" in tab.target}<Avatar kind="session" name={tab.label} root={tab.root} size={16} />{/if}{tab.label}
          {#if tab.exited}<span class="muted">exited{tab.exited.code === undefined ? "" : ` ${tab.exited.code}`}</span>{/if}</button>
        <button type="button" class="x" aria-label={`Close ${tab.label}`} title="Close" onclick={() => void close(tab.key)}><Icon name="close" size={12} /></button></span>{/each}
      {#if jobs.length}<span class="tab" class:active={active === JOBS}>
        <button type="button" role="tab" class="jobs" aria-selected={active === JOBS} onclick={() => { only = undefined; void show(JOBS); }}>Jobs
          {#if running}<span class="muted">{running}</span>{/if}
          {#if unopened}<span class="muted">·</span><StatusPill tone="failed" word label="{unopened} failed" />{/if}</button></span>{/if}
    </div>
    <span class="menu">
      <button type="button" bind:this={plus} aria-label="New terminal" title="New terminal" aria-haspopup="menu" aria-expanded={picking}
        onclick={() => { if (picking) picking = false; else void pick(); }}><Icon name="plus" /></button>
      {#if picking}<div class="list" role="menu" tabindex="-1" bind:this={menu} onkeydown={walk}>
        {#if sessions.length}<p class="label">Sessions</p>{/if}
        {#each sessions as session (session.id)}<button type="button" role="menuitem" tabindex="-1" onclick={() => open({ box: session.id }, session.name, session.root)}>{session.name}</button>{/each}
        {#if hosts.length}<p class="label">Hosts</p>{/if}
        {#each hosts as host (host.name)}<button type="button" role="menuitem" tabindex="-1" onclick={() => open({ host: host.name }, hostLabel(host.name))}>{hostLabel(host.name)}{#if host.diskLow}<StatusPill tone="warning" label="Disk low" />{/if}</button>{/each}
      </div>{/if}
    </span>
    {#if shown}<span class="where" title={where}>{where}</span>{/if}
    {#if active}<button type="button" class="tool" aria-label={floating ? "Dock the terminals" : "Float the terminals"} title={floating ? "Dock" : "Float"}
      onclick={() => { floating = !floating; }}><Icon name={floating ? "dockBottom" : "undock"} /></button>
      {#if !floating}<button type="button" class="tool" aria-label={maximized ? "Restore the terminals" : "Maximize the terminals"} title={maximized ? "Restore" : "Maximize"}
        onclick={() => { maximized = !maximized; collapsed = false; }}><Icon name={maximized ? "restore" : "maximize"} /></button>
      <button type="button" class="tool fold" class:collapsed aria-label={collapsed ? "Expand the terminals" : "Collapse the terminals"} title={collapsed ? "Expand" : "Collapse"}
        aria-expanded={!collapsed} onclick={() => { collapsed = !collapsed; if (!collapsed) void show(active); }}><Icon name="chevron" /></button>{/if}{/if}
  {/snippet}
  {#if !floating && active && !collapsed && !maximized}<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (a separator that the arrow keys move is focusable) -->
    <div class="grip" role="separator" aria-orientation="horizontal" aria-label="Terminal height" aria-valuenow={height} tabindex="0" onpointerdown={drag}
      onkeydown={event => { const step = { ArrowUp: 24, ArrowDown: -24 }[event.key]; if (step) { event.preventDefault(); height = fit(height + step); } }}></div>{/if}
  <!-- Keys typed into a shell stay there, so Escape closes no panel and Alt with an arrow key moves no window; F6 still moves between areas. -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="body" hidden={!active || !floating && collapsed} style:height={floating ? null : `${maximized ? tallest : height}px`} onkeydown={event => { if (event.key !== "F6") event.stopPropagation(); }}>
    {#each tabs as tab (tab.key)}<div class="tabpanel" hidden={tab.key !== active}>
      {#if tab.exited}<p class="exited">{tab.exited.code ? `Exited with code ${tab.exited.code}` : "The shell exited."}
        <button type="button" class="retry" onclick={() => renew(tab)}>New shell</button></p>{/if}
      <TermPane bind:this={panes[tab.key]} target={tab.target} label={tab.label} onexit={code => { tab.exited = { code }; }} />
    </div>{/each}
    <!-- Mounted only while open, so its watches close with it (Jobs fix 1). -->
    {#if active === JOBS && (floating || !collapsed)}<div class="tabpanel">
      <JobsPane bind:this={jobsPane} bind:only sessions={jobs} {opened} onrunning={rows => { listed = rows; }} /></div>{/if}
  </div>
</FloatWindow>
{/if}

<style>
  :global(.terminals) { flex: none; border-top: 1px solid var(--line); background: var(--bg); }
  :global(.terminals:not(.floating) > header) { height: 30px; padding-left: 4px; }
  .tabs { display: flex; align-items: center; gap: 2px; min-width: 0; overflow-x: auto; scrollbar-width: none; }
  .tab { display: flex; align-items: center; flex: none; height: 24px; border-radius: 6px; }
  .tab:hover, .tab.active { background: var(--selected); }
  .tabs [role="tab"] { display: flex; align-items: center; width: auto; padding: 0 2px 0 8px; gap: 6px; color: var(--muted); font-size: 12.5px; font-weight: 500; white-space: nowrap; }
  .tab.active [role="tab"] { color: var(--text); }
  .tabs [role="tab"]:hover { background: none; }
  .muted { color: var(--muted); font-weight: 400; }
  .jobs { padding-right: 8px; }
  .x { width: 20px; height: 20px; margin-right: 3px; }
  .where { flex: 0 1 auto; min-width: 0; overflow: hidden; color: var(--muted); font-size: 12.5px; font-weight: 400; white-space: nowrap; text-overflow: ellipsis; }
  @media (max-width: 640px) { .where { display: none; } }
  /* The dock's own controls sit at the right, 28 px like a floating window's. */
  .tool { width: 28px; height: 28px; }
  .fold :global(svg) { transition: transform 120ms; }
  .fold.collapsed :global(svg) { transform: rotate(180deg); }
  .menu { position: relative; margin-right: auto; }
  .list { position: absolute; left: 0; bottom: calc(100% + 4px); z-index: 4; min-width: 200px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px rgb(0 0 0 / 18%); }
  :global(.floating) .list { bottom: auto; top: calc(100% + 4px); }
  .list .label { margin: 6px 8px 2px; }
  .list [role="menuitem"] { width: auto; height: auto; justify-content: space-between; gap: 12px; display: flex; padding: 6px 8px; color: var(--text); font-size: 14px; }
  .list [role="menuitem"]:focus { background: var(--hover); outline: none; }
  .grip { position: absolute; top: -3px; left: 0; right: 0; height: 6px; z-index: 1; cursor: ns-resize; touch-action: none; }
  .grip:hover, .grip:focus-visible { background: var(--focus); opacity: 0.6; outline: none; }
  /* Docked, the body keeps the dragged height; floating, it fills the window. */
  .body[hidden] { display: none; }
  .body { display: flex; flex-direction: column; min-height: 0; flex: none; }
  :global(.floating) .body { flex: 1; }
  .tabpanel { flex: 1; display: flex; flex-direction: column; min-height: 0; }
  .tabpanel[hidden] { display: none; }
  .exited { margin: 0; padding: 6px 12px; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 13px; }
  .exited .retry { margin-left: 8px; }
</style>
