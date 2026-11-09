<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import Icon from "./Icon.svelte";
  import MenuList from "./MenuList.svelte";
  import VirtualList from "./VirtualList.svelte";
  import { fileSize } from "../feed.ts";
  import { sharedMedia, sharedURL, type SharedEntry } from "../shared.ts";
  import { sharedChild, sharedRowKey, sharedRows, type SharedRow } from "../shared-tree.ts";
  import type { MediaItem } from "../media.ts";

  let { room, target, onopen }: { room: string; target?: { path: string; request: number }; onopen: (items: MediaItem[], initial: number) => void } = $props();
  const listings = new SvelteMap<string, SharedEntry[]>(), expanded = new SvelteSet<string>(), failures = new SvelteSet<string>();
  const pending = new Map<string, Promise<SharedEntry[] | undefined>>();
  let rows = $derived(sharedRows(listings, expanded, failures));
  let selected = $state(""), focusTarget = $state("");
  let list: VirtualList<SharedRow> | undefined = $state();
  let menu = $state<{ path: string; anchor: DOMRect; trigger: HTMLButtonElement; dir: boolean }>();
  let menuRoot = $state<HTMLDivElement>();
  let held: { timer: ReturnType<typeof setTimeout>; x: number; y: number } | undefined;
  let suppressUntil = 0;

  function load(path: string): Promise<SharedEntry[] | undefined> {
    if (listings.has(path)) return Promise.resolve(listings.get(path));
    const existing = pending.get(path);
    if (existing) return existing;
    failures.delete(path);
    const task = fetch(`/v1/rooms/${encodeURIComponent(room)}/shared/tree?${new URLSearchParams({ path })}`)
      .then(response => response.ok ? response.json() as Promise<{ entries: SharedEntry[] }> : undefined)
      .then(result => {
        if (!result) { failures.add(path); return; }
        listings.set(path, result.entries);
        return result.entries;
      }, () => { failures.add(path); return undefined; });
    pending.set(path, task);
    void task.then(() => pending.delete(path));
    return task;
  }
  onMount(() => { void load(""); });
  async function toggle(path: string) {
    if (expanded.has(path)) expanded.delete(path);
    else { expanded.add(path); await load(path); }
  }
  function open(row: Extract<SharedRow, { kind: "entry" }>) {
    if (Date.now() < suppressUntil) return;
    selected = row.path;
    if (row.entry.dir) { void toggle(row.path); return; }
    const items = (listings.get(row.parent) ?? []).filter(entry => !entry.dir).map(entry => {
      const path = sharedChild(row.parent, entry.name);
      return sharedMedia(room, path, { key: `shared:${path}`, from: { kind: "owner" }, at: entry.modified }, entry.size, entry.modified);
    });
    onopen(items, items.findIndex(item => item.sharedPath === row.path));
  }
  // The modal can point at a file not yet in the tree. Load just its ancestors, then reveal it.
  async function reveal(path: string) {
    const parts = path.split("/").filter(Boolean);
    let parent = "";
    await load(parent);
    for (const part of parts) {
      const entry = listings.get(parent)?.find(entry => entry.name === part);
      if (!entry) break;
      parent = sharedChild(parent, part);
      if (entry.dir) { expanded.add(parent); await load(parent); }
    }
    selected = parent;
    // Focus when the virtual row mounts, not before the scroll event renders its window.
    focusTarget = parent;
    await tick();
    const index = rows.findIndex(row => row.kind === "entry" && row.path === parent);
    if (index >= 0) await list?.reveal(index, "center");
  }
  $effect(() => { const request = target; if (request) void untrack(() => reveal(request.path)); });
  function showMenu(event: MouseEvent, row: Extract<SharedRow, { kind: "entry" }>) {
    event.preventDefault();
    const trigger = event.currentTarget as HTMLButtonElement;
    menu = { path: row.path, dir: row.entry.dir, trigger, anchor: event.clientX || event.clientY ? new DOMRect(event.clientX, event.clientY) : trigger.getBoundingClientRect() };
  }
  function closeMenu(refocus: boolean) { const trigger = menu?.trigger; menu = undefined; if (refocus && trigger?.isConnected) trigger.focus(); }
  function choose(action: string) {
    if (!menu) return;
    const path = menu.path;
    if (action === "copy") void navigator.clipboard.writeText(`/box/shared/${path}`);
    else {
      const link = document.createElement("a");
      link.href = sharedURL(room, path, true); link.download = path.split("/").at(-1)!;
      link.click();
    }
    closeMenu(true);
  }
  function release() { if (held) clearTimeout(held.timer); held = undefined; }
  onDestroy(release);
  function press(event: PointerEvent, row: Extract<SharedRow, { kind: "entry" }>) {
    if (event.pointerType !== "touch") return;
    const trigger = event.currentTarget as HTMLButtonElement;
    release();
    held = { x: event.clientX, y: event.clientY, timer: setTimeout(() => {
      held = undefined; suppressUntil = Date.now() + 700;
      menu = { path: row.path, dir: row.entry.dir, trigger, anchor: trigger.getBoundingClientRect() };
    }, 450) };
  }
  function drift(event: PointerEvent) { if (held && Math.hypot(event.clientX - held.x, event.clientY - held.y) > 10) release(); }
</script>

<svelte:window onpointerdown={event => { if (menu && event.target instanceof Node && !menuRoot?.contains(event.target) && !menu.trigger.contains(event.target)) closeMenu(false); }} />
<div class="files-tree">
  <VirtualList bind:this={list} items={rows} key={sharedRowKey} label="Room files" role="group" align="top" estimate={30}>
    {#snippet row(row: SharedRow)}
      {@const depth = Math.min(row.depth, 6)}
      {#if row.kind === "entry"}
        <div class="row" style:--depth={depth} class:selected={selected === row.path} title={`/box/shared/${row.path}`}>
          {#if row.entry.dir}<button type="button" class="fold" style:left={`${6 + depth * 16}px`} aria-expanded={expanded.has(row.path)} aria-label={`${expanded.has(row.path) ? "Collapse" : "Expand"} ${row.entry.name}`} onclick={() => void toggle(row.path)}><Icon name="chevron" size={12} /></button>{/if}
          <button type="button" class="file" style:padding-left={`${30 + depth * 16}px`} data-shared-row={row.path} aria-pressed={selected === row.path} {@attach node => { if (focusTarget === row.path) { node.focus({ preventScroll: true }); focusTarget = ""; } }} onclick={() => open(row)} oncontextmenu={event => showMenu(event, row)} onpointerdown={event => press(event, row)} onpointermove={drift} onpointerup={release} onpointercancel={release}>
            <Icon name={row.entry.dir ? "folder" : "file"} /><span class="name">{row.entry.name}</span>{#if !row.entry.dir}<span class="size">{fileSize(row.entry.size)}</span>{/if}
          </button>
        </div>
      {:else if row.kind === "error"}<button class="state" style:padding-left={`${30 + depth * 16}px`} onclick={() => void load(row.path)}>Didn't load. <span class="again">Try again</span></button>
      {:else}<div class="state" style:padding-left={`${30 + depth * 16}px`}>{row.kind === "empty" ? "Empty" : "Loading…"}</div>{/if}
    {/snippet}
  </VirtualList>
</div>
{#if menu}<div bind:this={menuRoot}><MenuList label={`Actions for ${menu.path.split("/").at(-1)}`} items={menu.dir ? ["copy"] : ["copy", "download"]} keyOf={action => action} anchor={menu.anchor} side="start" onchoose={choose} onclose={closeMenu}>
  {#snippet row(action)}<span>{action === "copy" ? "Copy path" : "Download"}</span>{/snippet}
</MenuList></div>{/if}

<style>
  .files-tree { display: flex; flex-direction: column; flex: 1; min-height: 0; overflow: hidden; }
  .files-tree :global(.virtual) { flex: 1; min-height: 0; }
  .row { position: relative; display: flex; align-items: center; }
  .file { display: flex; align-items: center; gap: 8px; width: 100%; min-width: 0; min-height: 30px; padding: 0 12px; border: 0; background: none; color: var(--text); font: inherit; font-size: 13px; text-align: left; cursor: pointer; }
  .file:hover { background: var(--hover); }
  .row.selected { background: var(--selected); }
  .file :global(svg) { flex: none; color: var(--muted); }
  .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .size { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .fold { all: unset; position: absolute; z-index: 1; top: 50%; transform: translateY(-50%); min-width: 16px; min-height: 24px; display: flex; align-items: center; justify-content: center; padding: 0 1px; border-radius: 4px; color: var(--muted); cursor: pointer; }
  .fold[aria-expanded="false"] :global(svg) { transform: rotate(-90deg); }
  .fold:hover { color: var(--text); }
  .file:focus-visible, .state:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .fold:focus-visible { outline: 2px solid var(--focus); outline-offset: 0; }
  .state { display: block; width: 100%; min-height: 30px; padding: 6px 12px; border: 0; background: none; color: var(--muted); font: inherit; font-size: 12.5px; text-align: left; }
  button.state { cursor: pointer; }
  /* The retry reads as the one action in the line, as the Settings lists' Try again does. */
  .again { color: var(--text); text-decoration: underline; text-underline-offset: 3px; }
  button.state:hover .again { color: var(--focus); }
  @media (hover: none) { .fold { width: 24px; min-height: 30px; } }
</style>
