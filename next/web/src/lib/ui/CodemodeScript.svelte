<script lang="ts">
  import DOMPurify from "dompurify";
  import type { TranscriptItem, TranscriptPart, TranscriptCall } from "../contract.gen.ts";
  import { stepTitle } from "../steps.ts";
  import { formatTimelineDuration } from "../timeline.ts";
  import { highlight } from "../highlight.ts";
  import Icon from "./Icon.svelte";
  import TranscriptIcon from "./TranscriptIcon.svelte";
  import StatusPill from "./StatusPill.svelte";

  let { item, ended, chunks, onload, onopen }: {
    item: TranscriptItem; ended: boolean; chunks: Record<string, string>;
    onload: (item: TranscriptItem, part: TranscriptPart) => Promise<void>;
    onopen: () => void;
  } = $props();
  let allCalls = $state(false), allScript = $state(false), allOutput = $state(false);
  let copyState: "copy" | "check" | "warning" = $state("copy");
  let failedLoad = $state(false), loadingParts = $state(false);
  let script = $derived(chunks[item.id + "/script"] ?? item.script ?? "");
  let output = $derived(chunks[item.id + "/output"] ?? item.output ?? "");
  let lines = $derived(script.split("\n"));
  let outputLines = $derived(output.split("\n"));
  let highlighted: string[] = $state([]);
  $effect(() => {
    const source = allScript ? lines : lines.slice(0, 12);
    let valid = true;
    void Promise.all(source.map(line => highlight(line, "javascript"))).then(result => {
      if (valid) highlighted = result.map(line => DOMPurify.sanitize(line));
    });
    return () => { valid = false; };
  });
  let shown = $derived((item.calls ?? []).filter((call, index) => allCalls || (item.calls?.length ?? 0) <= 6 || index < 5 || call.status === "error"));
  let rows = $derived.by(() => {
    const result: { calls: TranscriptCall[]; title: ReturnType<typeof stepTitle> }[] = [];
    for (const call of shown) {
      const title = stepTitle({ tool: call.tool, label: call.target ?? "" });
      const last = result.at(-1);
      if (title.kind === "read" && last?.title.kind === "read" && call.status === "ok" && last.calls.every(c => c.status === "ok")) last.calls.push(call);
      else result.push({ calls: [call], title });
    }
    return result;
  });
  async function expand() {
    const missing = (["script", "output"] as const).filter(part => chunks[item.id + "/" + part] === undefined);
    if (!item.more || loadingParts || !missing.length) return;
    loadingParts = true;
    try { await Promise.all(missing.map(part => onload(item, part))); failedLoad = false; }
    catch { failedLoad = true; }
    finally { loadingParts = false; }
  }
  function copy() {
    navigator.clipboard.writeText(script).then(() => copyState = "check", () => copyState = "warning").then(() => setTimeout(() => copyState = "copy", 1500));
  }
</script>

<div class="script-group">
  {#each rows as row}
    <div class="call">
      <div class="call-line"><span class="slot"><TranscriptIcon kind={["run", "search", "read", "edit", "write"].includes(row.title.kind) ? row.title.kind : "read"} /></span>
        <span>{row.title.kind === "tool" ? `Called ${row.title.verb.toLowerCase()}` : row.title.verb}</span><span class="target">{row.calls.map(call => call.target).filter(Boolean).join(", ")}</span>
        {#if row.calls[0].status === "error"}<StatusPill tone="failed" label="Failed" word />{:else if row.calls[0].status === "cancelled"}<span>Stopped</span>{/if}
        {#if row.calls.some(call => call.durationMs !== undefined)}<span class="time">{formatTimelineDuration(row.calls.reduce((sum, call) => sum + (call.durationMs ?? 0), 0))}</span>{/if}
      </div>
      {#if row.calls[0].status === "error" && row.calls[0].error}<div class="error">{row.calls[0].error.split("\n")[0]}</div>{/if}
    </div>
  {/each}
  {#if !allCalls && (item.calls?.length ?? 0) > 6}<button class="more" onclick={() => allCalls = true}>Show all {item.calls?.length}</button>{/if}
  {#if item.callsOmitted}<div class="more">{item.callsOmitted} more actions not shown</div>{/if}
  {#if item.failed && output}<div class="error">{outputLines[0]}</div>{/if}
  <details ontoggle={event => { if (event.currentTarget.open) { onopen(); void expand(); } }}>
    <summary><span class="slot"><Icon name="prompt" size={14} /></span>
      <span>{!item.ended && !ended ? "Running script" : item.calls?.length ? `Script · ${lines.length} ${lines.length === 1 ? "line" : "lines"}${output ? ` · output ${outputLines.length} ${outputLines.length === 1 ? "line" : "lines"}` : ""}` : "Ran a script"}</span>
      {#if item.failed}<StatusPill tone="failed" label="Failed" word />{/if}
      <span class="chevron"><Icon name="chevron" size={14} /></span>
    </summary>
    <div class="body">
      {#if failedLoad}<button disabled={loadingParts} onclick={() => void expand()}>Could not load script. Try again</button>{/if}
      <div class="heading">Script <button class="copy" aria-label={copyState === "copy" ? "Copy code" : copyState === "check" ? "Copied" : "Could not copy"} onclick={copy}><Icon name={copyState} size={14} /></button></div>
      <pre>{#each (allScript ? lines : lines.slice(0, 12)) as line, index}<span class="number">{index + 1}</span> <span>{#if highlighted[index] !== undefined}{@html highlighted[index]}{:else}{line}{/if}</span>{"\n"}{/each}</pre>
      {#if lines.length > 12 && !allScript}<button onclick={() => allScript = true}>Show all</button>{/if}
      {#if output}<div class="heading">Output</div><pre>{(allOutput ? outputLines : outputLines.slice(0, 12)).join("\n")}</pre>{#if outputLines.length > 12 && !allOutput}<button onclick={() => allOutput = true}>Show all</button>{/if}{/if}
    </div>
  </details>
</div>

<style>
  .call-line, summary { display: flex; align-items: center; gap: 6px; min-height: 24px; }
  summary { cursor: pointer; list-style: none; }
  summary::-webkit-details-marker { display: none; }
  summary:hover { color: var(--text); background: var(--hover); }
  .slot { display: inline-flex; flex: none; width: 24px; justify-content: center; }
  .target { font-family: var(--mono); color: var(--text); overflow-wrap: anywhere; }
  .time, .chevron { margin-left: auto; white-space: nowrap; }
  .time { font-size: 11px; font-variant-numeric: tabular-nums; }
  .error { margin-left: 30px; color: var(--danger); overflow-wrap: anywhere; }
  .body { margin-left: 30px; }
  .heading { display: flex; align-items: center; margin: 8px 0; }
  .copy { margin-left: auto; width: 24px; height: 24px; display: grid; place-items: center; }
  pre { margin: 0; padding: 10px; background: var(--sidebar); border: 1px solid var(--line); border-radius: 6px; white-space: pre-wrap; overflow-wrap: anywhere; color: var(--text); font: 12.5px/1.5 var(--mono); }
  .number { color: var(--muted); user-select: none; }
  button { border: 0; background: none; color: var(--muted); font: inherit; cursor: pointer; }
  button:hover { color: var(--text); }
  button:focus-visible, summary:focus-visible { outline: 2px solid var(--focus); }
  .more { margin-left: 30px; }
</style>
