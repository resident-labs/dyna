<script lang="ts">
  import Markdown from "./Markdown.svelte";
  import MediaFile from "./MediaFile.svelte";
  import { parseTable, previewNote, readPreview, tableDelimiter, type MediaKind, type Preview } from "../media.ts";
  import type { Attachment } from "../contract.gen.ts";
  // The viewer's modes that read the file's text: markdown, a table, plain text, and every mode's source.
  let { file, src, kind, rendered }: { file: Attachment; src: string; kind: MediaKind; rendered: boolean } = $props();
  // undefined while it loads; null when the bytes are not text.
  let preview = $state<Preview | null>();
  let failed = $state(false), attempt = $state(0);
  $effect(() => {
    attempt;
    const controller = new AbortController();
    preview = undefined; failed = false;
    readPreview(src, controller.signal).then(result => { preview = result; }, () => { if (!controller.signal.aborted) failed = true; });
    return () => controller.abort();
  });
  let delimiter = $derived(tableDelimiter(file));
  let table = $derived(rendered && kind === "table" && delimiter && preview ? parseTable(preview.text, delimiter, preview.truncated) : undefined);
  let note = $derived(preview ? previewNote(file, preview, table) : "");
  let columns = $derived(Array.from({ length: table?.width ?? 0 }, (_, column) => column));
</script>

{#if failed}<div class="state">The file did not load. <button onclick={() => attempt++}>Retry</button></div>
{:else if preview === null}<MediaFile {file} {src} reason="This file is not text, so it has no preview." />
{:else if preview === undefined}<div class="state">Loading…</div>
{:else if !preview.text}<div class="state">This file is empty.</div>
{:else}
  <div class="reader">
    {#if note}<p class="note" role="status">{note}</p>{/if}
    <!-- Each mode scrolls in its own focusable box, so the keyboard can scroll it. -->
    {#if table?.rows.length}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="scroll table" tabindex="0" role="region" aria-label={`${file.name} table`}>
        <table>
          <thead><tr>{#each columns as column}<th scope="col">{table.rows[0][column] ?? ""}</th>{/each}</tr></thead>
          <tbody>{#each table.rows.slice(1) as row}<tr>{#each columns as column}<td>{row[column] ?? ""}</td>{/each}</tr>{/each}</tbody>
        </table>
      </div>
    {:else if rendered && kind === "markdown"}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="scroll" tabindex="0" role="region" aria-label={file.name}><div class="page"><Markdown rhythm="thread" text={preview.text} /></div></div>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <pre class="scroll" tabindex="0" role="region" aria-label={file.name}>{preview.text}</pre>
    {/if}
  </div>
{/if}

<style>
  .reader { display: flex; flex-direction: column; width: 100%; height: 100%; min-height: 0; background: var(--bg); }
  .note { flex: none; margin: 0; padding: 8px 16px; border-bottom: 1px solid var(--line); background: var(--raised); color: var(--muted); font-size: 12.5px; }
  .scroll { flex: 1; min-height: 0; margin: 0; overflow: auto; }
  .scroll:focus-visible { outline: 2px solid var(--focus); outline-offset: -2px; }
  .page { max-width: 760px; margin: 0 auto; padding: 24px; font-size: 15px; }
  pre { padding: 16px; color: var(--text); font: 12.5px/1.6 var(--mono); font-variant-ligatures: none; white-space: pre-wrap; overflow-wrap: anywhere; }
  /* Columns keep their width and the box scrolls sideways, so a phone does not fold every short cell. */
  table { width: max-content; min-width: 100%; border-collapse: separate; border-spacing: 0; font-size: 13px; font-variant-numeric: tabular-nums; }
  th, td { max-width: 40ch; padding: 6px 12px; border-bottom: 1px solid var(--line); text-align: left; vertical-align: top; overflow-wrap: anywhere; }
  /* border-collapse would let the border scroll away from the header. */
  th { position: sticky; top: 0; background: var(--raised); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
  tbody tr:hover { background: var(--hover); }
  .state { padding: 24px; color: var(--muted); font-size: 13px; }
  .state button { padding: 0; border: 0; background: none; color: inherit; font: inherit; text-decoration: underline; cursor: pointer; }
  @media (max-width: 640px) { .page { padding: 16px; } pre { padding: 12px; } }
</style>
