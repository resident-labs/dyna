<script lang="ts">
 import {dataChildren,dataCount,dataText,displayJSONStrings,type DataNode} from "../post-data.ts";
 import JsonTree from "./JsonTree.svelte";
 import ClampedText from "./ClampedText.svelte";
 let { value, label, index = false, root = false }: { value: DataNode; label?: string; index?: boolean; root?: boolean } = $props();
 // The root opens one level when the data chip opens; descendants always start folded.
 // svelte-ignore state_referenced_locally
 let open = $state(root);
 let limit = $state(50);
 const page = 50;
 let array = $derived(value.kind === "array");
 let collection = $derived(array || value.kind === "object");
 let count = $derived(dataCount(value));
 let entries = $derived(open && collection ? dataChildren(value,limit) : []);
 let countText = $derived(`${count} ${array ? "items" : "keys"}`);
</script>
{#snippet field()}{#if label !== undefined}{#if index}<span class="index">{label}</span>{:else}<span class="key">{displayJSONStrings(label)}</span>{/if}: {/if}{/snippet}
{#if collection}
 <details {open} ontoggle={event => { open = event.currentTarget.open; }} class:root>
  <summary>{@render field()}<span class="bracket">{array ? "[" : "{"}</span> <span class="count">{countText}</span> <span class="bracket">{array ? "]" : "}"}</span></summary>
  {#if open}<div class="children">{#each entries as entry (entry.value.start)}<JsonTree value={entry.value} label={entry.label} index={entry.index} />{/each}
   {#if count > limit}<button type="button" onclick={() => { limit += page; }}>Show next {Math.min(page, count - limit)} · {count - limit} remaining</button>{/if}
  </div>{/if}
 </details>
{:else}
 <div class="leaf">{#if value.kind === "string"}<ClampedText text={displayJSONStrings(dataText(value))} prefix={field} />{:else}{@render field()}<span>{dataText(value)}</span>{/if}</div>
{/if}
<style>
 details, .leaf { font: 12px/1.7 var(--mono); overflow-wrap: anywhere; min-width: 0; }
 summary { cursor: pointer; border-radius: 3px; }
 summary:hover { background: var(--hover); }
 summary:focus-visible, button:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
 .children { padding-left: 14px; border-left: 1px solid var(--line); margin-left: 4px; }
 .key { color: var(--focus); } .count, .bracket, .index { color: var(--muted); }
 button { color: var(--focus); background: none; border: 0; font: inherit; cursor: pointer; padding: 4px; text-align: left; }
 .leaf { white-space: normal; }
 @media (max-width: 640px) { .children { padding-left: 8px; margin-left: 2px; } }
</style>
