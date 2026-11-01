<!-- Ported from T3 Code (apps/web/src/components/chat/WorkLog.tsx), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE. -->
<script lang="ts">
  import type { TranscriptItem, TranscriptPart } from "../contract.gen.ts";
  import { activeVerb, elapsed, stepTime, titleOf, type StepKind } from "../steps.ts";
  import { summarizeWork } from "../timeline.ts";
  import Icon, { type IconName } from "./Icon.svelte";
  import TranscriptIcon from "./TranscriptIcon.svelte";
  import Markdown from "./Markdown.svelte";
  import StatusPill from "./StatusPill.svelte";

  let { items, ended, started, now, onload, onopen }: {
    items: TranscriptItem[];
    ended: boolean;
    started: string;
    now: number;
    onload: (item: TranscriptItem, part: TranscriptPart) => Promise<string>;
    onopen: () => void;
  } = $props();

  const icons: Record<StepKind, IconName> = { read: "file", edit: "edit", write: "write", search: "search", list: "list", run: "terminal", fetch: "globe", tool: "tool" };
  let chunks: Record<string, string> = $state({});
  let failures: Record<string, boolean> = $state({});
  let loading: Record<string, boolean> = $state({});
  let openDiff: Record<string, boolean> = $state({});
  const key = (item: TranscriptItem, part: TranscriptPart) => item.id + "/" + part;
  async function load(item: TranscriptItem, part: TranscriptPart) {
    const id = key(item, part);
    if (loading[id] || chunks[id] !== undefined) return;
    loading[id] = true;
    try { chunks[id] = await onload(item, part); failures[id] = false; }
    catch { failures[id] = true; }
    finally { loading[id] = false; }
  }

  // Dyna keeps thoughts and tools in transcript order. The caller splits at posts and other boundaries.
  let active = $derived(!ended && items.some(item => !item.ended));
  let tools = $derived(items.filter(item => item.kind === "tool"));
  let summary = $derived(active ? `Working for ${elapsed(started, now)}` : tools.length ? summarizeWork(items) : summarizeWork(items));
</script>

{#snippet entry(item: TranscriptItem)}
  {@const title = titleOf(item)}
  {@const running = !ended && !item.ended}
  <details class="entry" ontoggle={(event) => { if (event.currentTarget.open) { onopen(); if (item.more) { if (item.kind === "thinking") void load(item, "text"); else { void load(item, "arguments"); void load(item, "output"); if (item.diff) void load(item, "diff"); } } } }}>
    <summary>
      <span class="slot">{#if running}<span class="spinner" aria-label="Working" role="img"></span>{:else}{#if item.kind === "thinking" || ["run", "search", "read", "edit", "write"].includes(title.kind)}<TranscriptIcon kind={item.kind === "thinking" ? "thinking" : title.kind} />{:else}<Icon name={icons[title.kind]} size={16} />{/if}{/if}</span>
      {#if item.kind === "thinking"}<span>Thought</span>{:else if running}<span>{activeVerb(title.kind, title.verb)}</span><span class="target" title={title.full}>{title.target}</span>{:else if title.kind === "run" || title.kind === "edit" || title.kind === "write"}<span class="target" title={title.full}>{title.target || title.verb}</span>{:else}<span>{title.verb}</span><span class="target" title={title.full}>{title.target}</span>{/if}
      <span class="time">{running ? elapsed(item.at, now) : stepTime({ started: item.at, ended: item.ended })}</span>
      {#if item.failed}<StatusPill tone="failed" label="Failed" word />{/if}
      <span class="entry-chevron"><TranscriptIcon kind="chevron" /></span>
    </summary>
    <div class="content">
      {#if item.kind === "thinking"}
        <Markdown text={chunks[key(item, "text")] ?? item.text} />
        {#if item.more && chunks[key(item, "text")] === undefined}
          <button disabled={loading[key(item, "text")]} onclick={() => void load(item, "text")}>{failures[key(item, "text")] ? "Could not load thought. Try again" : "Show all"}</button>
        {/if}
      {:else}
        {#if item.diff}
          {@const diff = chunks[key(item, "diff")] ?? item.diff}
          {@const lines = diff.split("\n")}
          {#if !item.failed}
            <div class="diff-summary"><code>{title.target}</code><span class="add">+{lines.filter(line => line.startsWith("+") && !line.startsWith("+++")).length}</span><span class="remove">−{lines.filter(line => line.startsWith("-") && !line.startsWith("---")).length}</span><button onclick={() => { openDiff[item.id] = !openDiff[item.id]; }}>{openDiff[item.id] ? "Close diff" : "Open diff"}</button></div>
          {/if}
          {#if item.failed || openDiff[item.id]}<pre class="diff">{#each lines as line}<span class:add={line.startsWith("+")} class:remove={line.startsWith("-")}>{line}&#10;</span>{/each}</pre>{/if}
        {/if}
        {#if !item.diff || item.failed}
        {#each ["arguments", "output"] as part}
          {@const name = part as "arguments" | "output"}
          {#if item[name] || chunks[key(item, name)] !== undefined}<pre>{chunks[key(item, name)] ?? item[name]}</pre>{/if}
          {#if item.more && chunks[key(item, name)] === undefined}
            <button disabled={loading[key(item, name)]} onclick={() => void load(item, name)}>{failures[key(item, name)] ? `Could not load ${name}. Try again` : `Show all ${name}`}</button>
          {/if}
        {/each}
        {/if}
      {/if}
    </div>
  </details>
{/snippet}

{#if items.length === 1}
  {@render entry(items[0])}
{:else if items.length > 1}
  <details class="group" open={!ended} ontoggle={(event) => { if (event.currentTarget.open) onopen(); }}>
    <summary><span class="slot"><TranscriptIcon kind="hammer" /></span><span>{summary}</span><span class="entry-chevron"><TranscriptIcon kind="chevron" /></span></summary>
    <div class="entries" role="region" aria-label="Work details">
      {#each items as item (item.id)}{@render entry(item)}{/each}
    </div>
  </details>
{/if}

<style>
  .group, .entry { color: var(--muted); font-size: 14px; line-height: 1.625; }
  summary { display: flex; align-items: center; min-height: 24px; gap: 6px; cursor: pointer; list-style: none; border-radius: 6px; }
  summary::-webkit-details-marker { display: none; }
  summary:hover { color: var(--text); background: var(--hover); }
  summary:focus-visible, button:focus-visible { outline: 2px solid var(--focus); }
  .slot { width: 24px; display: inline-flex; justify-content: center; align-items: center; flex: none; }
  .entry-chevron { margin-left: auto; display: inline-flex; }
  details[open] > summary .entry-chevron { transform: rotate(90deg); }
  details:not([open]) > summary .time { display: none; }
  .target { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .entries { max-height: min(18rem, 50dvh); overflow: auto; overscroll-behavior: contain; }
  .content { margin-left: 28px; padding: 4px 0; max-height: 384px; overflow: auto; }
  code { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 12px var(--mono); }
  pre { max-height: 256px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; font: 11px/1.625 var(--mono); }
  .diff-summary { display: flex; gap: 8px; align-items: center; }
  .diff-summary button { margin-left: auto; }
  .add, .diff .add { color: var(--success, #65a972); }
  .remove, .diff .remove { color: var(--danger, #d97b7b); }
  .time { margin-left: auto; white-space: nowrap; font-size: 11px; font-variant-numeric: tabular-nums; }
  button { border: 0; padding: 0; background: none; color: inherit; font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  .spinner { width: 12px; height: 12px; border: 1.5px solid currentColor; border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner, .entry-chevron { animation: none; transition: none; } }
</style>
