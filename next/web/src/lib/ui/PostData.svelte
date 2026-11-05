<script lang="ts">
 import type { JSONValue, PostDataRef } from "../contract.gen.ts";
 import { postData } from "../post-data.ts";
 import { fileSize } from "../feed.ts";
 import JsonTree from "./JsonTree.svelte";
 import ClampedText from "./ClampedText.svelte";
 let { value, reference }: { value?: JSONValue; reference?: PostDataRef | null } = $props();
 let loaded = $state<JSONValue | undefined>(undefined);
 let raw = $state<string | undefined>(undefined);
 let current = $derived(value === undefined ? loaded : value);
 let model = $derived(current === undefined ? undefined : postData(current));
 let open = $state(false);
 let copied = $state("");
 let failure = $state("");
 let loading = $state(false);
 let referenceHash = $derived(reference?.file.sha256);
 // A transcript refresh may replace the metadata object for the same immutable file.
 $effect(() => { referenceHash; loaded = undefined; raw = undefined; failure = ""; });
 function load() {
  if (!reference || loading) return;
  const hash = reference.file.sha256;
  loading = true; failure = "";
  fetch(`/v1/files/${encodeURIComponent(hash)}`).then(response => {
   if (!response.ok) throw new Error("Data did not load");
   return response.text();
  }).then(text => ({ text, value: JSON.parse(text) as JSONValue })).then(result => {
   if (reference?.file.sha256 === hash) { loaded = result.value; raw = result.text; }
  }, () => { failure = "Data did not load"; }).finally(() => { loading = false; });
 }
 async function copy() {
  await navigator.clipboard.writeText(raw ?? model!.json).then(() => { copied = "Copied"; }, () => { copied = "Could not copy"; });
 }
 function download() {
  const url = URL.createObjectURL(new Blob([raw ?? model!.json], { type: "application/json;charset=utf-8" }));
  const link = document.createElement("a"); link.href = url; link.download = "data.json"; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
 }
</script>
<section class="data" aria-label="Workflow data">
 {#if !reference && model?.inline}
  <div class="inline"><div class="inline-value">{#if typeof current === "string"}<ClampedText text={`data: ${JSON.stringify(current)}`} />{:else}<code title={JSON.stringify(current)}>data: {JSON.stringify(current)}</code>{/if}</div><button type="button" class="copy" aria-label="Copy JSON" onclick={copy}>{copied || "Copy"}</button><span class="status" role="status">{copied}</span></div>
 {:else}
  <details {open} ontoggle={event => { open = event.currentTarget.open; if (open && current === undefined) load(); }}>
   <summary>data <span aria-hidden="true">·</span> {fileSize(reference?.file.bytes ?? model?.bytes ?? 0)} <span aria-hidden="true">·</span> {reference?.summary ?? model?.summary}</summary>
   {#if open}<div class="expanded">
    {#if current !== undefined}<div class="tools"><button type="button" onclick={copy}>Copy JSON</button><button type="button" onclick={download}>Download</button><span role="status">{copied}</span></div>
     <!-- svelte-ignore a11y_no_noninteractive_tabindex (The bounded tree is focusable so keyboard users can scroll it.) -->
     <div class="tree-scroll" role="region" aria-label="JSON data tree" tabindex="0"><JsonTree value={current} root /></div><p class="hint">Expand collections to explore · scroll for more</p>
    {:else if failure}<p role="alert">{failure}. <button type="button" onclick={load}>Retry</button></p>
    {:else}<p role="status">Loading data…</p>{/if}
   </div>{/if}
  </details>
 {/if}
</section>
<style>
 .data { margin: 8px 0; max-width: 100%; color: var(--text); }
 summary { display: list-item; width: fit-content; max-width: 100%; padding: 6px 10px; border: 1px solid var(--line); border-radius: 7px; color: var(--muted); background: var(--sidebar); cursor: pointer; font-size: 12px; }
 summary:hover { color: var(--text); background: var(--hover); }
 summary:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
 .expanded { border: 1px solid var(--line); border-radius: 7px; padding: 10px 12px; margin-top: 6px; }
 .tree-scroll { max-height: 55vh; overflow: auto; scrollbar-width: thin; }
 .tree-scroll:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
 .hint { font-size: 11px; color: var(--muted); margin: 8px 0 0; }
 .tools { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; margin-bottom: 6px; color: var(--muted); font-size: 12px; }
 button { border: 0; padding: 3px 0; color: var(--muted); background: none; font: inherit; cursor: pointer; }
 button:hover { color: var(--text); text-decoration: underline; }
 .inline { display: flex; align-items: start; gap: 8px; color: var(--muted); font: 12px/1.7 var(--mono); }
 .inline-value { min-width: 0; flex: 1; }
 code { display: block; font: inherit; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
 .copy { opacity: 0; width: 48px; text-align: right; font: 11px/1.7 var(--mono); flex: none; }
 .inline .status { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
 .inline:hover .copy, .inline:focus-within .copy { opacity: 1; }
 .status { font-size: 11px; }
</style>
