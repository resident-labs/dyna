<script lang="ts">
  import { tick } from "svelte";
  import { call } from "../client.ts";
  import type { HostSummary } from "../contract.gen.ts";
  import { hostLabel, machines } from "../hosts.ts";
  import type { TerminalTarget } from "../terminal.ts";
  import FloatWindow from "./FloatWindow.svelte";
  import Icon from "./Icon.svelte";
  import Avatar from "./Avatar.svelte";
  import JobOutput from "./JobOutput.svelte";
  import StatusPill from "./StatusPill.svelte";
  import TermPane from "./TermPane.svelte";
  import { failed, jobTitle } from "../jobs.ts";
  import { ShellWatches, type ShellJob } from "../shell.ts";
  import { wholeTabs } from "../strip.ts";
  // The owner's terminals: a 34 px strip under the composer, always there in a room, with "+ Terminal" and one tab per
  // open shell or job output (dock design C). A tab opens its pane above the strip, which never moves; docked, the pane
  // sits above the strip, and floating, it is a window over the app with the strip's place held. The dock is app-wide:
  // hosts are not room objects, so tabs stay across rooms and on Home. A tab lives while the page is open (no reattach
  // in M2); x closes its socket, and the engine ends the shell. "+ Terminal" lists the room's sessions whose box is
  // ready, then the hosts. A job the header's Jobs tool opens is a tab too, named by its title, with the failed dot
  // when it failed; each job tab's session is watched while the page is visible. Only whole tabs show, the rest behind
  // "N more". On a phone the strip is 40 px with tabs that scroll sideways, and a tab opens full screen with Back and
  // Close, and for a shell the key row (v1's Esc, Tab, Ctrl and arrows).
  type Session = { id: string; name: string; root: boolean };
  let { sessions, terminal, phone, strip, element = $bindable(), onempty }: { sessions: Session[]; terminal: boolean; phone: boolean; strip: boolean; element?: HTMLElement; onempty: () => void } = $props();
  type Tab = { key: number; label: string; root: boolean; input?: boolean; exited?: { code?: number } } & ({ target: TerminalTarget } | { session: Session; job: string });
  type Pane = { focus(): void; key?: TermPane["key"] };
  let tabs: Tab[] = $state([]);
  let active = $state(0);
  let next = 1;
  let picking = $state(false);
  let rest = $state(false);
  let hosts: HostSummary[] = $state([]);
  let floating = $state(false);
  let rect = $state({ x: window.innerWidth - 744, y: window.innerHeight - 464, w: 720, h: 440 });
  // The docked height, kept for the page view. Maximized, the dock takes the most height fit allows; with no tab active, only its strip shows.
  let height = $state(280);
  let maximized = $state(false);
  let full = $state(false);
  let ctrl = $state(false);
  let menu: HTMLElement | undefined = $state();
  let plus: HTMLButtonElement | undefined = $state();
  const panes: Record<number, Pane | undefined> = $state({});
  let visible = $state(document.visibilityState === "visible");
  const watches = new ShellWatches();
  $effect(() => { watches.sync(new Set(visible ? tabs.flatMap(tab => "job" in tab ? [tab.session.id] : []) : [])); });
  $effect(() => () => watches.sync(new Set()));
  const jobOf = (tab: Tab) => "job" in tab ? watches.jobs.get(tab.session.id)?.get(tab.job) : undefined;
  export function hasInput(): boolean { return tabs.some(tab => tab.input); }
  // A job opens in its own tab, or shows the tab it already has.
  export function openJob(session: Session, job: ShellJob) {
    const tab = tabs.find(item => "job" in item && item.session.id === session.id && item.job === job.job);
    if (tab) { void show(tab.key); return; }
    tabs.push({ key: next++, label: jobTitle(job), root: session.root, session, job: job.job });
    void show(tabs[tabs.length - 1].key);
  }

  // Opens the "+ Terminal" menu, or the terminal itself when there is only one to choose. The machine running the
  // engine counts once, as on Home.
  async function pick() {
    rest = false;
    hosts = await call("host.list", {}).then(list => machines(list.hosts ?? []), () => []);
    if (sessions.length + hosts.length === 1) {
      if (hosts[0]) open2({ host: hosts[0].name }, hostLabel(hosts[0].name));
      else open2({ box: sessions[0].id }, sessions[0].name, sessions[0].root);
      return;
    }
    picking = true;
    await tick();
    // With no item (no ready session, no hosts), the menu itself takes the keys.
    (menu?.querySelector<HTMLElement>("[role=menuitem]") ?? menu)?.focus();
  }
  // Showing a tab opens its pane (on a phone, full screen) and moves focus into its pane.
  async function show(key: number) {
    active = key;
    full = phone;
    rest = false;
    await tick();
    panes[key]?.focus();
  }
  const liveShell = (id: string) => tabs.find(tab => "target" in tab && "box" in tab.target && tab.target.box === id && !tab.exited);
  // Whether the session has a shell open, for the DM header's pressed Terminal button.
  export const hasShell = (id: string) => liveShell(id) !== undefined;
  // The DM header's Terminal button: it shows the session's shell, or opens one. The "+" menu always opens a new one.
  export function shell(id: string, name: string, root: boolean) {
    const tab = liveShell(id);
    if (tab) void show(tab.key); else open2({ box: id }, name, root);
  }
  function open2(target: TerminalTarget, label: string, root = false) {
    picking = false;
    tabs.push({ key: next++, target, label, root });
    void show(tabs[tabs.length - 1].key);
  }
  // A new shell for an exited tab reopens its target under a new key, so its pane starts over.
  function renew(tab: Tab & { target: TerminalTarget }) {
    const index = tabs.findIndex(item => item.key === tab.key);
    tabs[index] = { key: next++, target: tab.target, label: tab.label, root: tab.root };
    void show(tabs[index].key);
  }
  // Focus moves to the tab that takes the closed one's place, or out of the dock when none is left.
  async function close(key: number) {
    const index = tabs.findIndex(tab => tab.key === key);
    tabs.splice(index, 1);
    if (active === key) active = phone ? 0 : tabs[Math.min(index, tabs.length - 1)]?.key ?? 0;
    if (!active) full = false;
    if (!tabs.length) floating = false;
    await tick();
    if (!active) { if (picking) plus?.focus(); else onempty(); }
    else await show(active);
  }
  // Up and Down move in the menu, Escape closes it and returns to "+".
  function walk(event: KeyboardEvent) {
    const items = Array.from(menu?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? []);
    const at = items.findIndex(item => item === document.activeElement);
    const to = { ArrowDown: at + 1, ArrowUp: at - 1 }[event.key];
    if (to !== undefined) { event.preventDefault(); items[(to + items.length) % items.length]?.focus(); }
    if (event.key === "Escape") { event.stopPropagation(); picking = false; rest = false; (menu?.closest(".menu")?.querySelector("button"))?.focus(); }
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
  let where = $derived(shown && "target" in shown ? "box" in shown.target ? `${shown.label} box` : shown.label : undefined);
  $effect(() => { if (!phone) full = false; });
  let open = $derived(active !== 0 && (phone ? full : true));
  // The strip measures its tabs, hidden ones too, and shows the whole tabs that fit; a phone's strip scrolls instead.
  let rail: HTMLElement | undefined = $state();
  let room = $state(0);
  let sizes: number[] = $state([]);
  const measure = () => { if (rail) { room = rail.clientWidth; sizes = Array.from(rail.children, child => child instanceof HTMLElement ? child.offsetWidth : 0); } };
  $effect(() => { const el = rail; if (!el) return; const observer = new ResizeObserver(measure); observer.observe(el); return () => observer.disconnect(); });
  $effect(() => { void tabs.map(tab => tab.label + (tab.exited ? " exited" : "")).join(); void tick().then(measure); });
  let shownTabs = $derived(phone || sizes.length !== tabs.length ? tabs.map((_, index) => index) : wholeTabs(sizes, room, tabs.findIndex(tab => tab.key === active)));
  let hidden = $derived(tabs.filter((_, index) => !shownTabs.includes(index)));
</script>

<svelte:window onresize={() => { viewport = window.innerHeight; }} onpointerdown={event => { if (!(event.target instanceof Element && event.target.closest(".menu"))) { picking = false; rest = false; } }} />
<svelte:document onvisibilitychange={() => { visible = document.visibilityState === "visible"; }} />

{#snippet tabList(list: Tab[])}{#each list as tab (tab.key)}{@const job = jobOf(tab)}{@const off = !shownTabs.includes(tabs.indexOf(tab))}<span class="tab" class:active={tab.key === active} class:off>
  <button type="button" role="tab" aria-selected={tab.key === active} onclick={() => void show(tab.key)}>{#if "target" in tab && "box" in tab.target}<Avatar kind="session" name={tab.label} root={tab.root} size={16} />
    {:else if job && failed(job)}<StatusPill tone="failed" dot label="Failed" />{/if}<span class="label">{tab.label}{"target" in tab ? " shell" : ""}</span>
    {#if tab.exited}<span class="muted">exited{tab.exited.code === undefined ? "" : ` ${tab.exited.code}`}</span>{/if}</button>
  <button type="button" class="x" aria-label={`Close ${tab.label}`} title="Close" onclick={() => void close(tab.key)}><Icon name="close" size={12} /></button></span>{/each}{/snippet}

{#if strip || tabs.length}
{#if floating}<div class="holder"></div>{/if}
<FloatWindow label="Terminals" kind={`terminals${open ? " open" : ""}${phone ? " phone" : ""}`} docked={!floating && !full} {full} bind:rect bind:element>
  {#snippet header()}
    {#if full && shown}
      <button type="button" aria-label="Back" title="Back" onclick={() => { full = false; }}><Icon name="backChevron" /></button>
      {#if "target" in shown && "box" in shown.target}<Avatar kind="session" name={shown.label} root={shown.root} size={16} />{:else if jobOf(shown) && failed(jobOf(shown)!)}<StatusPill tone="failed" dot label="Failed" />{/if}
      <strong class="full-title">{shown.label}{"target" in shown ? " shell" : ""}</strong>
      <button type="button" aria-label={`Close ${shown.label}`} title="Close" onclick={() => void close(shown!.key)}><Icon name="close" /></button>
    {:else}
    <span class="menu">
      {#if terminal}<button type="button" class="add" bind:this={plus} aria-label="New terminal" title="New terminal" aria-haspopup="menu" aria-expanded={picking}
        onclick={() => { if (picking) picking = false; else void pick(); }}><Icon name="plus" size={14} />Terminal</button>{/if}
      {#if picking}<div class="list" role="menu" tabindex="-1" bind:this={menu} onkeydown={walk}>
        {#if sessions.length}<p class="label">Sessions</p>{/if}
        {#each sessions as session (session.id)}<button type="button" role="menuitem" tabindex="-1" onclick={() => open2({ box: session.id }, session.name, session.root)}>{session.name}</button>{/each}
        {#if hosts.length}<p class="label">Hosts</p>{/if}
        {#each hosts as host (host.name)}<button type="button" role="menuitem" tabindex="-1" onclick={() => open2({ host: host.name }, hostLabel(host.name))}>{hostLabel(host.name)}{#if host.diskLow}<StatusPill tone="warning" label="Disk low" />{/if}</button>{/each}
      </div>{/if}
    </span>
    {#if tabs.length}<span class="rule"></span>{/if}
    <div class="tabs" role="tablist" aria-label="Terminals" bind:this={rail}>{@render tabList(tabs)}</div>
    {#if hidden.length}<span class="menu">
      <button type="button" class="add" aria-haspopup="menu" aria-expanded={rest} onclick={async () => { rest = !rest; picking = false; await tick(); menu?.querySelector<HTMLElement>("[role=menuitem]")?.focus(); }}>{hidden.length} more<Icon name="chevron" size={12} /></button>
      {#if rest}<div class="list" role="menu" tabindex="-1" bind:this={menu} onkeydown={walk}>
        {#each hidden as tab (tab.key)}<button type="button" role="menuitem" tabindex="-1" onclick={() => void show(tab.key)}>{tab.label}{"target" in tab ? " shell" : ""}</button>{/each}
      </div>{/if}
    </span>{/if}
    <span class="space"></span>
    {#if shown && !phone}<span class="where" title={where}>{where}</span>{/if}
    {#if open && !phone}<button type="button" class="tool" aria-label={floating ? "Dock the terminals" : "Float the terminals"} title={floating ? "Dock" : "Float"}
      onclick={() => { floating = !floating; }}><Icon name={floating ? "dockBottom" : "undock"} /></button>
      {#if !floating}<button type="button" class="tool" aria-label={maximized ? "Restore the terminals" : "Maximize the terminals"} title={maximized ? "Restore" : "Maximize"}
        onclick={() => { maximized = !maximized; }}><Icon name={maximized ? "restore" : "maximize"} /></button>{/if}
      <button type="button" class="tool fold" aria-label="Collapse the terminals" title="Collapse"
        aria-expanded="true" onclick={() => { floating = false; active = 0; }}><Icon name="chevron" /></button>{/if}
    {/if}
  {/snippet}
  {#if !floating && open && !phone && !maximized}<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions (a separator that the arrow keys move is focusable) -->
    <div class="grip" role="separator" aria-orientation="horizontal" aria-label="Terminal height" aria-valuenow={height} tabindex="0" onpointerdown={drag}
      onkeydown={event => { const step = { ArrowUp: 24, ArrowDown: -24 }[event.key]; if (step) { event.preventDefault(); height = fit(height + step); } }}></div>{/if}
  <!-- Keys typed into a shell stay there, so Escape closes no panel and Alt with an arrow key moves no window; F6 still moves between areas. -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="body" hidden={!open} style:height={floating || full ? null : `${maximized ? tallest : height}px`} onkeydown={event => { if (event.key !== "F6") event.stopPropagation(); }}>
    {#each tabs as tab (tab.key)}<div class="tabpanel" hidden={tab.key !== active}>
      {#if "job" in tab}<JobOutput bind:this={panes[tab.key]} session={tab.session} job={jobOf(tab)} label={tab.label} />
      {:else}
        {#if tab.exited}<p class="exited">{tab.exited.code ? `Exited with code ${tab.exited.code}` : "The shell exited."}
          <button type="button" class="retry" onclick={() => renew(tab)}>New shell</button></p>{/if}
        <TermPane bind:this={panes[tab.key]} bind:ctrl target={tab.target} label={tab.label} oninput={() => { tab.input = true; }} onexit={code => { tab.exited = { code }; }} />
      {/if}
    </div>{/each}
    {#if full && shown && "target" in shown}<div class="keys" role="toolbar" aria-label="Terminal keys">
      {#each [["esc", "Esc"], ["tab", "Tab"]] as const as [name, text] (name)}<button type="button" onpointerdown={event => event.preventDefault()} onclick={() => panes[active]?.key?.(name)}>{text}</button>{/each}
      <button type="button" aria-pressed={ctrl} onpointerdown={event => event.preventDefault()} onclick={() => { ctrl = !ctrl; }}>Ctrl</button>
      {#each [["left", "←", "Left"], ["up", "↑", "Up"], ["down", "↓", "Down"], ["right", "→", "Right"]] as const as [name, glyph, text] (name)}<button type="button" aria-label={text} onpointerdown={event => event.preventDefault()} onclick={() => panes[active]?.key?.(name)}>{glyph}</button>{/each}
    </div>{/if}
  </div>
</FloatWindow>
{/if}

<style>
  /* Docked, the pane sits above the strip: the strip is the window's header, which column-reverse moves to the bottom. */
  :global(.terminals) { flex: none; border-top: 1px solid var(--line); background: var(--bg); }
  :global(.terminals:not(.floating)) { flex-direction: column-reverse; }
  :global(.terminals:not(.floating) > header) { height: 34px; padding-left: 6px; border-bottom: 0; }
  :global(.terminals.open:not(.floating) > header) { border-top: 1px solid var(--line); }
  :global(.terminals.phone:not(.floating) > header) { height: 40px; }
  :global(.terminals.floating > header) { padding-left: 6px; }
  /* A floating window leaves its strip's place, so the feed does not move. */
  .holder { flex: none; height: 34px; border-top: 1px solid var(--line); }
  .tabs { position: relative; flex: 0 1 auto; display: flex; align-items: center; gap: 2px; min-width: 0; overflow: hidden; }
  :global(.terminals.phone) .tabs { flex: 1; overflow-x: auto; scrollbar-width: none; }
  .tab { display: flex; align-items: center; flex: none; height: 26px; border-radius: 6px; }
  :global(.terminals.phone) .tab { height: 32px; }
  .tab:hover, .tab.active { background: var(--selected); }
  /* A tab that does not fit stays measurable but out of sight, and out of the tab order. */
  .tab.off { position: absolute; visibility: hidden; pointer-events: none; }
  .tabs [role="tab"] { display: flex; align-items: center; width: auto; padding: 0 2px 0 8px; gap: 6px; color: var(--muted); font-size: 12.5px; font-weight: 500; white-space: nowrap; }
  .tab.active [role="tab"] { color: var(--text); }
  .tabs [role="tab"]:hover { background: none; }
  .muted { color: var(--muted); font-weight: 400; }
  /* A tab's name truncates at 220 px. */
  .label { max-width: 220px; overflow: hidden; text-overflow: ellipsis; }
  .x { width: 20px; height: 20px; margin-right: 3px; }
  .rule { flex: none; width: 1px; height: 16px; margin: 0 4px; background: var(--line); }
  .space { flex: 1; }
  .where { flex: 0 1 auto; min-width: 0; overflow: hidden; color: var(--muted); font-size: 12.5px; font-weight: 400; white-space: nowrap; text-overflow: ellipsis; }
  @media (max-width: 640px) { .where { display: none; } }
  /* The dock's own controls sit at the strip's right end, 28 px like a floating window's. */
  .tool { width: 28px; height: 28px; }
  .fold :global(svg) { transform: rotate(0deg); }
  .menu { position: relative; flex: none; }
  .menu .add { display: flex; align-items: center; gap: 4px; width: auto; height: 26px; padding: 0 8px; font-size: 12.5px; font-weight: 500; white-space: nowrap; }
  :global(.terminals.phone) .menu .add { height: 32px; padding: 0 10px; }
  .list { position: absolute; left: 0; bottom: calc(100% + 4px); z-index: 4; min-width: 200px; padding: 4px; display: grid;
    background: var(--raised); border: 1px solid var(--line); border-radius: 8px; box-shadow: 0 6px 20px var(--shadow-md); }
  :global(.floating) .list { bottom: auto; top: calc(100% + 4px); }
  .list .label { margin: 6px 8px 2px; }
  .list [role="menuitem"] { width: auto; height: auto; justify-content: space-between; gap: 12px; display: flex; padding: 6px 8px; color: var(--text); font-size: 14px; }
  .list [role="menuitem"]:focus { background: var(--hover); outline: none; }
  .full-title { flex: 1; min-width: 0; overflow: hidden; color: var(--text); font-size: 15px; text-overflow: ellipsis; white-space: nowrap; }
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
  /* v1's key row above the phone keyboard; a held Ctrl shows the focus ring. */
  .keys { flex: none; display: flex; gap: 6px; padding: 8px 12px calc(8px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); overflow-x: auto; }
  .keys button { all: unset; box-sizing: border-box; flex: none; display: grid; place-items: center; width: 44px; height: 40px; padding: 0 4px; border-radius: 8px; box-shadow: inset 0 0 0 1px var(--firm);
    color: var(--text); font-size: 14px; font-weight: 500; cursor: pointer; }
  .keys button:active { background: var(--hover); }
  .keys button[aria-pressed="true"] { box-shadow: inset 0 0 0 2px var(--focus); }
  .keys button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
</style>
