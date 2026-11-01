<!-- Ported from T3 Code (apps/web/src/components/chat/WorkLog.tsx), MIT License, Copyright (c) 2026 T3 Tools Inc. See next/third_party/t3code/LICENSE. -->
<script lang="ts">
  import type { TranscriptItem, TranscriptPart } from "../contract.gen.ts";
  import { activeVerb, elapsed, stepTime, titleOf, type StepKind } from "../steps.ts";
  import { formatTimelineDuration, summarizeWork, timelineItemRunning } from "../timeline.ts";
  import Icon, { type IconName } from "./Icon.svelte";
  import TranscriptIcon from "./TranscriptIcon.svelte";
  import Markdown from "./Markdown.svelte";
  import StatusPill from "./StatusPill.svelte";
  import CodemodeScript from "./CodemodeScript.svelte";

  let { items, ended, started, now, chunks, onload, onopen }: {
    items: TranscriptItem[];
    ended: boolean;
    started: string;
    now: number;
    chunks: Record<string, string>;
    onload: (item: TranscriptItem, part: TranscriptPart) => Promise<void>;
    onopen: () => void;
  } = $props();

  const icons: Record<StepKind, IconName> = { read: "file", edit: "edit", write: "write", search: "search", list: "list", run: "terminal", fetch: "globe", tool: "tool" };
  const parts = ["arguments", "output"] as const;
  let failures: Record<string, boolean> = $state({});
  let loading: Record<string, boolean> = $state({});
  let openDiff: Record<string, boolean> = $state({});
  let expandedContent: Record<string, boolean> = $state({});
  const key = (item: TranscriptItem, part: TranscriptPart) => item.id + "/" + part;
  async function load(item: TranscriptItem, part: TranscriptPart) {
    const id = key(item, part);
    if (loading[id] || chunks[id] !== undefined) return;
    loading[id] = true;
    try { await onload(item, part); failures[id] = false; }
    catch { failures[id] = true; }
    finally { loading[id] = false; }
  }

  // Dyna keeps thoughts and tools in transcript order. The caller splits at posts and other boundaries.
  let active = $derived(items.some(item => timelineItemRunning(item, ended)));
  let summary = $derived(active ? `Working for ${formatTimelineDuration(now - Date.parse(started))}` : summarizeWork(items));
  let thoughts = $derived(items.filter(item => item.kind === "thinking"));
  let tools = $derived(items.filter(item => item.kind !== "thinking"));
  let failedCount = $derived(tools.reduce((count, item) => count + Number(!!item.failed) + (item.calls ?? []).filter(call => call.status === "error").length, 0));
  let showThoughts = $state(false);
</script>

{#snippet entry(item: TranscriptItem)}
  {@const title = titleOf(item)}
  {@const running = timelineItemRunning(item, ended)}
  {#if item.tool === "codemode"}
    <CodemodeScript {item} {ended} {chunks} {onload} />
  {:else}
  <details class="entry" ontoggle={(event) => { if (event.currentTarget.open) { onopen(); if (item.more) { if (item.kind === "thinking") void load(item, "text"); else { void load(item, "arguments"); void load(item, "output"); if (item.diff) void load(item, "diff"); } } } }}>
    <summary>
      <span class="slot">{#if running}<span class="spinner" aria-label="Working" role="img"></span>{:else}{#if item.kind === "thinking" || ["run", "search", "read", "edit", "write"].includes(title.kind)}<TranscriptIcon kind={item.kind === "thinking" ? "thinking" : title.kind} />{:else}<Icon name={icons[title.kind]} size={16} />{/if}{/if}</span>
      {#if item.kind === "thinking"}<span>Thought</span>{:else if running}<span class="target" title={title.full}>{activeVerb(title.kind, title.verb)} {title.target}</span>{:else if title.kind === "run" || title.kind === "edit" || title.kind === "write"}<span class="target" title={title.full}>{title.target || title.verb}</span>{:else}<span class="target" title={title.full}>{title.verb} {title.target}</span>{/if}
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
            <div class="diff-summary"><span class="diff-path">{title.target}</span><span class="add">+{lines.filter(line => line.startsWith("+") && !line.startsWith("+++")).length}</span><span class="remove">−{lines.filter(line => line.startsWith("-") && !line.startsWith("---")).length}</span><button onclick={() => { openDiff[item.id] = !openDiff[item.id]; }}>{openDiff[item.id] ? "Close diff" : "Open diff"}</button></div>
          {/if}
          {#if item.failed || openDiff[item.id]}<pre class="diff">{#each (expandedContent[item.id + "/diff"] ? lines : lines.slice(0, 12)) as line}<span class:add={line.startsWith("+")} class:remove={line.startsWith("-")}>{line}&#10;</span>{/each}</pre>{#if lines.length > 12 && !expandedContent[item.id + "/diff"]}<button onclick={() => expandedContent[item.id + "/diff"] = true}>Show all</button>{/if}{/if}
        {/if}
        {#if !item.diff || item.failed}
        {#each parts as name}
          {#if item[name] || chunks[key(item, name)] !== undefined}{@const lines = (chunks[key(item, name)] ?? item[name] ?? "").split("\n")}<pre>{(expandedContent[key(item, name)] ? lines : lines.slice(0, 12)).join("\n")}</pre>{#if lines.length > 12 && !expandedContent[key(item, name)]}<button onclick={() => expandedContent[key(item, name)] = true}>Show all</button>{/if}{/if}
          {#if item.more && chunks[key(item, name)] === undefined}
            <button disabled={loading[key(item, name)]} onclick={() => void load(item, name)}>{failures[key(item, name)] ? `Could not load ${name}. Try again` : `Show all ${name}`}</button>
          {/if}
        {/each}
        {/if}
      {/if}
    </div>
  </details>
  {/if}
{/snippet}

{#if items.length === 1 && items[0].kind === "tool" && items[0].tool !== "codemode"}
  <div class="group">{@render entry(items[0])}</div>
{:else if items.length}
  <div class="group">
    <div class="group-heading"><span class="slot"><TranscriptIcon kind="hammer" /></span><span>{summary}</span>
      {#if failedCount}<StatusPill tone="failed" label={`${failedCount} failed`} word />{/if}
      {#if thoughts.length}<button class="thought-toggle" aria-expanded={showThoughts} onclick={() => showThoughts = !showThoughts}>{thoughts.length} {thoughts.length === 1 ? "thought" : "thoughts"} <TranscriptIcon kind="chevron" /></button>{/if}
    </div>
    {#if showThoughts}{#each thoughts as thought (thought.id)}<div class="thought">{(thought.text ?? "").split("\n")[0]}</div>{/each}{/if}
    <div class="entries" role="region" aria-label="Work details">
      {#each tools as item (item.id)}{@render entry(item)}{/each}
    </div>
  </div>
{/if}

<style>
  .group, .entry { color: var(--muted); font-size: 14px; line-height: 1.625; }
  .group { margin-left: 16px; padding-left: 8px; border-left: 1px solid var(--line); }
  .group-heading { display: flex; align-items: center; gap: 6px; }
  .thought-toggle { margin-left: auto; display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
  .thought { margin-left: 28px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @media (max-width: 600px) { .group { margin-left: 8px; } }
  summary { display: flex; align-items: center; min-height: 24px; gap: 6px; cursor: pointer; list-style: none; border-radius: 6px; }
  summary::-webkit-details-marker { display: none; }
  summary:hover { color: var(--text); background: var(--hover); }
  summary:focus-visible, button:focus-visible { outline: 2px solid var(--focus); }
  .slot { width: 24px; display: inline-flex; justify-content: center; align-items: center; flex: none; }
  .entry-chevron { margin-left: auto; display: inline-flex; }
  details[open] > summary .entry-chevron { transform: rotate(90deg); margin-left: 0; }
  details:not([open]) > summary .time { display: none; }
  .target { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .content { margin-left: 28px; padding: 4px 0; }
  pre { white-space: pre-wrap; overflow-wrap: anywhere; font: 11px/1.625 var(--mono); }
  .diff-summary { display: flex; gap: 6px; align-items: center; font: inherit; }
  .diff-path { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .diff-summary button { border: 1px solid var(--line); border-radius: 6px; padding: 2px 6px; }
  .add, .diff .add { color: var(--success); }
  .remove, .diff .remove { color: var(--danger); }
  .time { margin-left: auto; white-space: nowrap; font-size: 11px; font-variant-numeric: tabular-nums; }
  button { border: 0; padding: 0; background: none; color: inherit; font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  .spinner { width: 12px; height: 12px; border: 1.5px solid currentColor; border-top-color: transparent; border-radius: 50%; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .spinner { animation: none; } }
</style>
