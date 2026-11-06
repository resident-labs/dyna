<script lang="ts">
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import Icon from "./Icon.svelte";
  import MediaContent from "./MediaContent.svelte";
  import MediaViewer from "./MediaViewer.svelte";
  import { fileSize } from "../feed.ts";
  import { hasSource, mediaKind, sourceToggle } from "../media.ts";
  import { filesURL, parentPath, sharedMedia, sharedURL, type SharedEntry } from "../shared.ts";
  let { room, path, navigate, onclose }: { room: string; path: string | undefined; navigate: (event: MouseEvent & { currentTarget: HTMLAnchorElement }) => void; onclose: () => void } = $props();
  const listings = new SvelteMap<string, SharedEntry[]>(), expanded = new SvelteSet<string>([""]);
  const loading = new SvelteSet<string>(), failures = new SvelteSet<string>();
  let selected = $state<SharedEntry>(), missing = $state(false), ready = $state(false), rendered = $state(true), viewer = $state(false), copied = $state(false);
  const childPath = (parent: string, name: string) => parent ? `${parent}/${name}` : name;
  async function load(path: string): Promise<SharedEntry[] | undefined> {
    if (listings.has(path)) return listings.get(path);
    loading.add(path); failures.delete(path);
    const response = await fetch(`/v1/rooms/${encodeURIComponent(room)}/shared/tree?${new URLSearchParams({ path })}`).catch(() => undefined);
    loading.delete(path);
    if (!response?.ok) { failures.add(path); return; }
    const result: { entries: SharedEntry[] } = await response.json();
    listings.set(path, result.entries);
    return result.entries;
  }
  async function toggle(path: string) {
    if (expanded.has(path)) expanded.delete(path);
    else { expanded.add(path); await load(path); }
  }
  $effect(() => {
    const current = path;
    let active = true;
    ready = false; missing = false; selected = undefined; rendered = true; copied = false;
    void (async () => {
      await load("");
      if (!active) return;
      if (current === undefined) { missing = true; ready = true; return; }
      const parts = current.split("/").filter(Boolean);
      let parent = "";
      for (let index = 0; index < parts.length; index++) {
        const entries = await load(parent);
        if (!active) return;
        const entry = entries?.find(entry => entry.name === parts[index]);
        if (!entry || index < parts.length - 1 && !entry.dir) { missing = true; ready = true; return; }
        expanded.add(parent);
        parent = childPath(parent, entry.name);
        if (index === parts.length - 1) {
          selected = entry;
          if (entry.dir) { expanded.add(parent); await load(parent); }
        }
      }
      if (active) ready = true;
    })();
    return () => { active = false; };
  });
  let file = $derived(selected && !selected.dir && path !== undefined ? sharedMedia(room, path, { key: path, from: { kind: "owner" }, at: selected.modified }, selected.size) : undefined);
  let folder = $derived(selected?.dir ? path ?? "" : parentPath(path ?? ""));
  let crumbs = $derived((path ?? "").split("/").filter(Boolean));
  function copy() { void navigator.clipboard.writeText(`/box/shared/${path ?? ""}`).then(() => copied = true); }
</script>

<div class="room-files" class:reading={!!file || missing}>
  <header class="bar">
    <button class="tool" title="Back to room" aria-label="Back to room" onclick={onclose}><Icon name="back" /></button><strong>Files</strong>
    <span class="location">/box/shared</span>
  </header>
  <div class="panes">
    <nav class="tree" aria-label="Room files">
      <a class="root" href={filesURL(room)} onclick={navigate}><Icon name="folder" />Shared files</a>
      {@render branch("", 0)}
    </nav>
    <section class="detail" aria-label="File preview">
      <div class="toolbar">
        <a class="tool phone" href={filesURL(room, folder)} onclick={navigate} aria-label="Back to folder" title="Back to folder"><Icon name="back" /></a>
        <nav class="breadcrumbs" aria-label="File path"><a href={filesURL(room)} onclick={navigate}>shared</a>{#each crumbs as part, index}<span>/</span>{#if index === crumbs.length - 1}<strong>{part}</strong>{:else}<a href={filesURL(room, crumbs.slice(0, index + 1).join("/"))} onclick={navigate}>{part}</a>{/if}{/each}</nav>
        {#if file}
          <div class="tools">
            {#if hasSource(mediaKind(file.file))}<button class="tool" title={sourceToggle(mediaKind(file.file), rendered)} aria-label={sourceToggle(mediaKind(file.file), rendered)} aria-pressed={!rendered} onclick={() => rendered = !rendered}><Icon name={rendered ? "code" : "eye"} /></button>{/if}
            <button class="tool" title={copied ? "Copied" : "Copy path"} aria-label={copied ? "Copied" : "Copy path"} onclick={copy}><Icon name={copied ? "check" : "copy"} /></button>
            <a class="tool" href={sharedURL(room, path!, true)} download={file.file.name} title="Download" aria-label="Download"><Icon name="download" /></a>
            <button class="tool" title="Open in viewer" aria-label="Open in viewer" onclick={() => viewer = true}><Icon name="expand" /></button>
          </div>
        {/if}
      </div>
      {#if !ready}<p class="state">Loading…</p>
      {:else if missing}<p class="state">Not found. <a href={filesURL(room)} onclick={navigate}>Back to shared files</a></p>
      {:else if file}<div class="preview">{#key file.src}<MediaContent item={file} {rendered} />{/key}</div>
      {:else}<div class="listing">{@render listing(path ?? "")}</div>{/if}
    </section>
  </div>
</div>

{#snippet branch(parent: string, depth: number)}
  {#if failures.has(parent)}<div class="state">The folder did not load. <button onclick={() => void load(parent)}>Retry</button></div>
  {:else if !listings.has(parent)}<p class="state">Loading…</p>
  {:else if !listings.get(parent)!.length}<p class="state">This folder is empty.</p>
  {:else}
    {#each listings.get(parent)! as entry}
      {@const full = childPath(parent, entry.name)}
      <div class="row" class:selected={path === full} style:padding-left={`${12 + depth * 16}px`} title={new Date(entry.modified).toLocaleString()}>
        {#if entry.dir}<button class="disclose" aria-label={`${expanded.has(full) ? "Collapse" : "Expand"} ${entry.name}`} aria-expanded={expanded.has(full)} onclick={() => void toggle(full)}><Icon name={expanded.has(full) ? "chevron" : "forwardChevron"} size={12} /></button>{:else}<span class="indent"></span>{/if}
        <a href={filesURL(room, full)} onclick={navigate} aria-current={path === full ? "page" : undefined}><Icon name={entry.dir ? "folder" : "file"} /><span class="name">{entry.name}</span><span class="size">{fileSize(entry.size)}</span></a>
      </div>
      {#if entry.dir && expanded.has(full)}{@render branch(full, depth + 1)}{/if}
    {/each}
  {/if}
{/snippet}

{#snippet listing(parent: string)}
  {#if failures.has(parent)}<p class="state">The folder did not load. <button onclick={() => void load(parent)}>Retry</button></p>
  {:else if !listings.has(parent)}<p class="state">Loading…</p>
  {:else if !listings.get(parent)!.length}<p class="state">This folder is empty.</p>
  {:else}{#each listings.get(parent)! as entry}
    <a class="listed" href={filesURL(room, childPath(parent, entry.name))} onclick={navigate} title={new Date(entry.modified).toLocaleString()}><Icon name={entry.dir ? "folder" : "file"} /><span class="name">{entry.name}</span><span class="size">{fileSize(entry.size)}</span></a>
  {/each}{/if}
{/snippet}

{#if viewer && file}<MediaViewer items={[file]} initial={0} {room} name={() => "you"} root={() => false} loadVisual={async () => []} onclose={() => viewer = false} onjump={() => {}} />{/if}

<style>
  .room-files { position: absolute; inset: 0; z-index: 4; display: flex; flex-direction: column; background: var(--bg); color: var(--text); }
  .bar { display: flex; align-items: center; gap: 12px; flex: none; height: 56px; padding: 0 16px; border-bottom: 1px solid var(--line); }
  .bar strong { font-size: 16px; font-weight: 600; }
  .location { color: var(--muted); font: 12.5px var(--mono); }
  .panes { display: flex; flex: 1; min-height: 0; }
  .tree { width: 280px; flex: none; padding: 12px 0; border-right: 1px solid var(--line); overflow: auto; background: var(--sidebar); }
  a { color: inherit; text-decoration: none; }
  button { font: inherit; color: inherit; cursor: pointer; background: none; border: 0; }
  a:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .root { display: flex; gap: 8px; align-items: center; height: 36px; padding: 0 20px; margin-bottom: 4px; font-size: 13px; font-weight: 600; }
  .row { display: flex; align-items: center; height: 36px; padding-right: 12px; font-size: 13px; }
  .row:hover, .root:hover, .listed:hover, .tool:hover { background: var(--hover); }
  .row.selected { background: var(--hover); color: var(--text); }
  .row a { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1; height: 100%; }
  .row :global(svg), .listed :global(svg), .root :global(svg) { flex: none; color: var(--muted); }
  .disclose, .indent { display: grid; place-items: center; width: 20px; height: 32px; flex: none; padding: 0; }
  .name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .size { flex: none; color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .detail { display: flex; flex-direction: column; flex: 1; min-width: 0; min-height: 0; }
  .toolbar { display: flex; align-items: center; gap: 8px; min-height: 52px; padding: 8px 16px; border-bottom: 1px solid var(--line); }
  .breadcrumbs { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; min-width: 0; flex: 1; color: var(--muted); font-size: 13px; overflow-wrap: anywhere; }
  .breadcrumbs strong { color: var(--text); font-weight: 500; }
  .breadcrumbs a:hover { color: var(--text); }
  .tools { display: flex; flex: none; gap: 4px; }
  .tool { display: grid; place-items: center; flex: none; width: 32px; height: 32px; padding: 0; border-radius: 6px; color: var(--muted); }
  .preview { display: grid; grid-template: minmax(0, 1fr) / minmax(0, 1fr); place-items: center; flex: 1; min-height: 0; overflow: hidden; }
  .listing { overflow: auto; padding: 12px 16px; }
  .listed { display: flex; align-items: center; gap: 12px; height: 40px; padding: 0 12px; border-radius: 6px; font-size: 13px; }
  .state { padding: 16px; margin: 0; color: var(--muted); font-size: 13px; }
  .state a { color: var(--focus); } .state button { text-decoration: underline; }
  .phone { display: none; }
  @media (max-width: 760px) {
    .tree { width: 100%; border: 0; }
    .detail { display: none; }
    .reading .tree { display: none; }
    .reading .detail { display: flex; }
    .phone { display: grid; }
    .toolbar { flex-wrap: wrap; padding: 8px; }
    .breadcrumbs { font-size: 12.5px; }
    .tools { width: 100%; justify-content: flex-end; }
    .tool { width: 40px; height: 40px; }
    .bar { padding: 0 8px; }
  }
</style>
